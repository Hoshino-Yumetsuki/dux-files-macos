package devices

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/godbus/dbus/v5"
)

const (
	Topic         = "devices"
	coalesceDelay = 150 * time.Millisecond
)

type Publisher interface {
	Publish(topic string, data any)
}

type Bus interface {
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
}

type Service struct {
	native        func(context.Context, string, any, any) error
	nativeMu      sync.Mutex
	nativeDevices []Device

	bus     Publisher
	uid     uint32
	usage   *usageCache
	stop    chan struct{}
	stopped sync.Once

	mu        sync.RWMutex
	conn      Bus
	objects   Objects
	available bool
	reason    string
}

type Snapshot struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Devices   []Device `json:"devices"`
}

func NewService(bus Publisher, uid uint32) *Service {
	reason := "udisks2 is not running"
	if runtime.GOOS != "linux" {
		reason = "udisks2 is unavailable on this platform"
	}
	return &Service{
		bus:     bus,
		uid:     uid,
		usage:   newUsageCache(),
		stop:    make(chan struct{}),
		objects: Objects{},
		reason:  reason,
	}
}

func (s *Service) Start() error {
	if runtime.GOOS == "darwin" {
		return s.startNative()
	}
	if runtime.GOOS != "linux" {
		s.setReason("udisks2 is unavailable on this platform")
		return nil
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		s.setReason("the system bus is unavailable")
		return err
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	matches := []dbus.MatchOption{dbus.WithMatchSender(BusName)}
	if err := conn.AddMatchSignal(matches...); err != nil {
		conn.Close()
		s.setReason("udisks2 signals are unavailable")
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, BusName),
	); err != nil {
		log.Debugf("devices: name watch unavailable: %v", err)
	}

	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	s.reload(conn)
	go s.loop(conn, signals)
	return nil
}

func (s *Service) Close() {
	s.stopped.Do(func() { close(s.stop) })
}

func (s *Service) loop(conn *dbus.Conn, signals chan *dbus.Signal) {
	defer conn.Close()
	var flush <-chan time.Time

	for {
		select {
		case <-s.stop:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if !s.apply(conn, sig) {
				continue
			}
			if flush == nil {
				flush = time.After(coalesceDelay)
			}
		case <-flush:
			flush = nil
			s.publish()
		}
	}
}

func (s *Service) apply(conn *dbus.Conn, sig *dbus.Signal) bool {
	switch sig.Name {
	case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
		return s.interfacesAdded(sig)
	case "org.freedesktop.DBus.ObjectManager.InterfacesRemoved":
		return s.interfacesRemoved(sig)
	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		return s.propertiesChanged(sig)
	case "org.freedesktop.DBus.NameOwnerChanged":
		s.reload(conn)
		s.publish()
		return false
	}
	return false
}

func (s *Service) interfacesAdded(sig *dbus.Signal) bool {
	if len(sig.Body) < 2 {
		return false
	}
	path, ok := sig.Body[0].(dbus.ObjectPath)
	if !ok {
		return false
	}
	added, ok := sig.Body[1].(map[string]map[string]dbus.Variant)
	if !ok {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects[path] == nil {
		s.objects[path] = map[string]map[string]dbus.Variant{}
	}
	for name, props := range added {
		s.objects[path][name] = props
	}
	return true
}

func (s *Service) interfacesRemoved(sig *dbus.Signal) bool {
	if len(sig.Body) < 2 {
		return false
	}
	path, ok := sig.Body[0].(dbus.ObjectPath)
	if !ok {
		return false
	}
	removed, _ := sig.Body[1].([]string)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range removed {
		delete(s.objects[path], name)
	}
	if len(s.objects[path]) == 0 {
		delete(s.objects, path)
	}
	return true
}

func (s *Service) propertiesChanged(sig *dbus.Signal) bool {
	if len(sig.Body) < 2 {
		return false
	}
	iface, _ := sig.Body[0].(string)
	changed, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	props := s.objects[sig.Path][iface]
	if props == nil {
		return false
	}
	for key, value := range changed {
		props[key] = value
		if key == "MountPoints" {
			for _, point := range allMountPoints(props) {
				s.usage.forget(point)
			}
		}
	}
	return true
}

func (s *Service) reload(conn Bus) {
	var objects Objects
	err := conn.Object(BusName, RootPath).Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.objects = Objects{}
		s.available = false
		s.reason = "udisks2 is not running"
		log.Debugf("devices: GetManagedObjects failed: %v", err)
		return
	}
	s.objects = objects
	s.available = true
	s.reason = ""
}

func (s *Service) setReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available = false
	s.reason = reason
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.native != nil {
		return Snapshot{Available: s.available, Reason: s.reason, Devices: append([]Device{}, s.nativeDevices...)}
	}
	return Snapshot{Available: s.available, Reason: s.reason, Devices: List(s.objects, s.uid)}
}

func (s *Service) publish() {
	if s.bus == nil {
		return
	}
	s.bus.Publish(Topic, s.Snapshot())
}

func (s *Service) find(id string) (Device, bool) {
	for _, device := range s.Snapshot().Devices {
		if device.ID == id {
			return device, true
		}
	}
	return Device{}, false
}

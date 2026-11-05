package filemanager1

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	Name      = "org.freedesktop.FileManager1"
	Path      = dbus.ObjectPath("/org/freedesktop/FileManager1")
	Interface = "org.freedesktop.FileManager1"
)

// nautilus data/freedesktop-dbus-interfaces.xml minus its two property extensions.
const introspection = `<node>
  <interface name="org.freedesktop.FileManager1">
    <method name="ShowFolders">
      <arg type="as" name="URIs" direction="in"/>
      <arg type="s" name="StartupId" direction="in"/>
    </method>
    <method name="ShowItems">
      <arg type="as" name="URIs" direction="in"/>
      <arg type="s" name="StartupId" direction="in"/>
    </method>
    <method name="ShowItemProperties">
      <arg type="as" name="URIs" direction="in"/>
      <arg type="s" name="StartupId" direction="in"/>
    </method>
  </interface>` + introspect.IntrospectDataString + `</node>`

type Handler interface {
	ShowFolders(uris []string) error
	ShowItems(uris []string) error
	ShowItemProperties(uris []string) error
}

type Status struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Owned     bool   `json:"owned"`
	Queued    bool   `json:"queued"`
	Owner     string `json:"owner"`
	Reason    string `json:"reason,omitempty"`
}

type Service struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	enabled bool
	reason  string
	native  func(context.Context, string, any, any) error
	state   string
}

type object struct{ handler Handler }

func (o object) ShowFolders(uris []string, _ string) *dbus.Error {
	return failed(o.handler.ShowFolders(uris))
}

func (o object) ShowItems(uris []string, _ string) *dbus.Error {
	return failed(o.handler.ShowItems(uris))
}

func (o object) ShowItemProperties(uris []string, _ string) *dbus.Error {
	return failed(o.handler.ShowItemProperties(uris))
}

func failed(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	return dbus.MakeFailedError(err)
}

func New(conn *dbus.Conn, handler Handler) (*Service, error) {
	if conn == nil {
		return nil, errors.New("no session bus")
	}
	if err := conn.Export(object{handler: handler}, Path, Interface); err != nil {
		return nil, err
	}
	if err := conn.Export(introspect.Introspectable(introspection), Path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}
	return &Service{conn: conn}, nil
}

func (s *Service) SetEnabled(enabled bool) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native != nil {
		return s.setNative(enabled)
	}
	switch {
	case enabled && !s.enabled:
		s.request()
	case !enabled && s.enabled:
		s.release()
	}
	return s.statusLocked()
}

func (s *Service) request() {
	reply, err := s.conn.RequestName(Name, 0)
	if err != nil {
		s.reason = err.Error()
		log.Warnf("filemanager1: request %s: %v", Name, err)
		return
	}
	s.enabled = true
	s.reason = ""
	if reply == dbus.RequestNameReplyInQueue {
		_, holder := s.ownerLocked()
		log.Infof("filemanager1: %s is held by %s; queued until it lets go", Name, holder)
	}
}

func (s *Service) release() {
	if _, err := s.conn.ReleaseName(Name); err != nil {
		log.Warnf("filemanager1: release %s: %v", Name, err)
	}
	s.enabled = false
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	if s.native != nil {
		return s.nativeStatus()
	}
	owned, holder := s.ownerLocked()
	return Status{
		Available: true,
		Enabled:   s.enabled,
		Owned:     owned,
		Queued:    s.enabled && !owned,
		Owner:     holder,
		Reason:    s.reason,
	}
}

func (s *Service) ownerLocked() (bool, string) {
	var unique string
	if err := s.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, Name).Store(&unique); err != nil {
		return false, ""
	}
	if names := s.conn.Names(); len(names) > 0 && names[0] == unique {
		return true, ""
	}
	var pid uint32
	if err := s.conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, unique).Store(&pid); err != nil {
		return false, unique
	}
	comm, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/comm")
	if err != nil {
		return false, unique
	}
	return false, strings.TrimSpace(string(comm))
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native != nil {
		return
	}
	if s.enabled {
		s.release()
	}
	s.conn.Close()
}

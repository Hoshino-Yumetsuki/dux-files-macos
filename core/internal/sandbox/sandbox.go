package sandbox

import (
	"maps"
	"runtime"
	"sync"

	"github.com/duxlabs-dev/dux-go/nativebridge"
)

const (
	ReasonSandboxed     = "SANDBOXED"
	ReasonNoHostFS      = "NOHOSTFS"
	ReasonNoUDisks      = "NOUDISKS"
	ReasonNoGvfs        = "NOGVFS"
	ReasonNoHostSpawn   = "NOHOSTSPAWN"
	ReasonPortalChooser = "PORTALCHOOSER"
)

const (
	portalDesktopDest   = "org.freedesktop.portal.Desktop"
	portalDesktopPath   = "/org/freedesktop/portal/desktop"
	portalDocumentsDest = "org.freedesktop.portal.Documents"
	portalDocumentsPath = "/org/freedesktop/portal/documents"
	portalIfacePrefix   = "org.freedesktop.portal."
)

// Reason is a stable code the UI maps to a sentence.
type Feature struct {
	Available bool   `json:"available"`
	Degraded  bool   `json:"degraded,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Boot-time permissions only; whether a permitted service is running is reported live by that service.
type Capabilities struct {
	Sandboxed      bool               `json:"sandboxed"`
	AppID          string             `json:"appId,omitempty"`
	HostFilesystem bool               `json:"hostFilesystem"`
	UserFilesystem bool               `json:"userFilesystem"`
	HostOSRoot     string             `json:"hostOsRoot,omitempty"`
	Filesystems    []string           `json:"filesystems"`
	UDisks         bool               `json:"udisks"`
	Gvfs           bool               `json:"gvfs"`
	HostSpawn      bool               `json:"hostSpawn"`
	Portals        map[string]uint32  `json:"portals"`
	Dirs           Dirs               `json:"dirs"`
	Features       map[string]Feature `json:"features"`
}

var (
	currentOnce sync.Once
	current     Capabilities
)

func Current() Capabilities {
	currentOnce.Do(func() {
		probe := newOSProbe()
		defer probe.close()
		current = Detect(probe)
		if runtime.GOOS == "darwin" {
			current.UDisks, current.Gvfs = false, false
			for _, name := range []string{"devices", "network", "elevation", "portalChooser", "setDefault", "indexSearch", "clipboard"} {
				current.Features[name] = Feature{Reason: "NOTSUPPORTED"}
			}
			current.Features["trash"] = Feature{Available: true, Degraded: true, Reason: "APPTRASH"}
			current.Features["openWith"] = Feature{Available: true, Degraded: true, Reason: "APPBUNDLEPATH"}
			if nativebridge.Available() {
				for _, name := range []string{"devices", "network", "setDefault", "indexSearch", "clipboard", "trash", "openWith"} {
					current.Features[name] = Feature{Available: true}
				}
			}
		}
	})
	return current
}

func Detect(p Probe) Capabilities {
	if p.Getenv("FLATPAK_ID") == "" {
		return native(p)
	}
	return sandboxed(p)
}

func native(p Probe) Capabilities {
	caps := Capabilities{
		HostFilesystem: true,
		UserFilesystem: true,
		Filesystems:    []string{},
		UDisks:         true,
		Gvfs:           true,
		HostSpawn:      true,
		Portals:        map[string]uint32{},
		Dirs:           nativeDirs(p.Getenv),
	}
	caps.Features = features(caps)
	return caps
}

func sandboxed(p Probe) Capabilities {
	data, _ := p.ReadFile(flatpakInfoPath)
	info := parseFlatpakInfo(data)
	if info.appID == "" {
		info.appID = p.Getenv("FLATPAK_ID")
	}

	caps := Capabilities{
		Sandboxed:   true,
		AppID:       info.appID,
		Filesystems: sortedKeys(info.filesystems),
		UDisks:      canTalk(info.systemBus, "org.freedesktop.UDisks2"),
		Gvfs:        gvfsPermitted(info),
		HostSpawn:   hostSpawnPermitted(info, p),
		Portals:     portalVersions(p),
	}
	// An override can drop `host` after the info file was written, so the mount is checked too.
	caps.HostFilesystem = info.hasFilesystem("host") && p.Exists(hostOSRoot+"/usr")
	caps.UserFilesystem = caps.HostFilesystem || info.hasFilesystem("home")
	caps.Dirs = nativeDirs(p.Getenv)
	if caps.HostFilesystem {
		caps.HostOSRoot = hostOSRoot
	}
	if caps.UserFilesystem {
		caps.Dirs = hostDirs(p.Getenv, caps.HostFilesystem)
	}
	caps.Features = features(caps)
	return caps
}

func gvfsPermitted(info flatpakInfo) bool {
	return canTalk(info.sessionBus, "org.gtk.vfs.Daemon") &&
		info.hasFilesystem("xdg-run/gvfs") &&
		info.hasFilesystem("xdg-run/gvfsd")
}

func hostSpawnPermitted(info flatpakInfo, p Probe) bool {
	if !canTalk(info.sessionBus, "org.freedesktop.Flatpak") {
		return false
	}
	_, err := p.LookPath("flatpak-spawn")
	return err == nil
}

func portalVersions(p Probe) map[string]uint32 {
	out := map[string]uint32{}
	for _, iface := range []string{"Trash", "OpenURI", "Settings"} {
		if v, ok := p.PortalVersion(portalDesktopDest, portalDesktopPath, portalIfacePrefix+iface); ok {
			out[iface] = v
		}
	}
	if v, ok := p.PortalVersion(portalDocumentsDest, portalDocumentsPath, portalIfacePrefix+"FileTransfer"); ok {
		out["FileTransfer"] = v
	}
	return out
}

func (c Capabilities) WithFeature(name string, f Feature) Capabilities {
	out := c
	out.Features = maps.Clone(c.Features)
	if out.Features == nil {
		out.Features = map[string]Feature{}
	}
	out.Features[name] = f
	return out
}

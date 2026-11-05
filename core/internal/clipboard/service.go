package clipboard

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/duxlabs-dev/dux-go/wlclipboard"
)

const (
	maxNativePaths     = 10000
	maxNativeSelection = 1 << 20
	maxNativeImage     = 32 << 20
)

type selection interface {
	Set(offers []wlclipboard.Offer) error
	Close()
}

type selector func(choose func([]string) string) ([]byte, string, error)

var imageFormats = []string{"image/png", "image/jpeg", "image/webp"}

var fileFormats = []string{MimeTextUTF8, "text/plain", "UTF8_STRING", "STRING", "TEXT", MimeGnomeCopiedFiles, MimeUriList}

// The owner holds a wayland connection and a goroutine, so it lives only from the first copy until clear or shutdown.
type Service struct {
	native   bool
	call     func(context.Context, string, any, any) error
	newOwner func() (selection, error)
	pick     selector

	mu       sync.Mutex
	owner    selection
	paths    []string
	cut      bool
	plain    string
	unusable bool
}

func NewService() *Service {
	return &Service{
		native:   runtime.GOOS == "darwin",
		unusable: runtime.GOOS != "linux" && runtime.GOOS != "darwin",
		newOwner: func() (selection, error) { return wlclipboard.NewOwner() },
		pick:     wlclipboard.PasteSelect,
	}
}

func (s *Service) Image() ([]byte, string, bool) {
	if s.native {
		var result struct {
			Data []byte `json:"data"`
			Mime string `json:"mime"`
		}
		if s.callNative("clipboard.image", nil, &result) != nil || len(result.Data) == 0 || len(result.Data) > maxNativeImage || !slices.Contains(imageFormats, result.Mime) {
			return nil, "", false
		}
		return result.Data, result.Mime, true
	}
	data, mime, err := s.pick(chooseImage)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	return data, mime, true
}

// An image-only clipboard is declined so no image crosses the pipe just to find no paths in it.
func chooseFiles(offered []string) string {
	for _, want := range fileFormats {
		if slices.Contains(offered, want) {
			return want
		}
	}
	return ""
}

func chooseImage(offered []string) string {
	for _, want := range imageFormats {
		if slices.Contains(offered, want) {
			return want
		}
	}
	for _, mime := range offered {
		if strings.HasPrefix(mime, "image/") {
			return mime
		}
	}
	return ""
}

func Handles(method string) bool { return strings.HasPrefix(method, "clipboard.") }

func (s *Service) Close() { _ = s.Clear() }

func (s *Service) Copy(paths []string, cut bool) error {
	if len(paths) == 0 {
		return errors.New("clipboard.copy requires paths")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return errors.New("clipboard.copy requires absolute paths: " + path)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native {
		if err := validateNativePaths(paths); err != nil {
			return err
		}
		return s.callNative("clipboard.copy", map[string]any{"paths": paths, "cut": cut}, nil)
	}
	if s.owner == nil {
		owner, err := s.newOwner()
		if err != nil {
			s.unusable = true
			return err
		}
		s.owner = owner
	}

	plain := string(PlainText(paths))
	if err := s.owner.Set(offers(paths, cut, plain)); err != nil {
		s.drop()
		return err
	}
	s.paths = append([]string(nil), paths...)
	s.cut = cut
	s.plain = plain
	return nil
}

func (s *Service) Paths() ([]string, bool, string) {
	if s.native {
		var result struct {
			Paths  []string `json:"paths"`
			Cut    bool     `json:"cut"`
			Source string   `json:"source"`
		}
		if s.callNative("clipboard.paths", nil, &result) != nil || len(result.Paths) == 0 || validateNativePaths(result.Paths) != nil {
			return nil, false, ""
		}
		return result.Paths, result.Cut && result.Source == "dfiles", result.Source
	}
	data, mime, err := s.pick(chooseFiles)
	if err != nil {
		return nil, false, ""
	}

	s.mu.Lock()
	mine := s.paths != nil && string(data) == s.plain
	paths, cut := append([]string(nil), s.paths...), s.cut
	s.mu.Unlock()

	if mine {
		return paths, cut, "dfiles"
	}
	foreign, foreignCut := Parse(data, mime)
	return foreign, foreignCut, mime
}

func (s *Service) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native {
		return s.callNative("clipboard.clear", nil, nil)
	}
	s.drop()
	return nil
}

// Stays true until a copy fails, so the UI never probes wayland at startup.
func (s *Service) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native {
		return nativebridge.Available()
	}
	return !s.unusable
}

func (s *Service) drop() {
	if s.owner != nil {
		s.owner.Close()
		s.owner = nil
	}
	s.paths = nil
	s.cut = false
	s.plain = ""
}

func offers(paths []string, cut bool, plain string) []wlclipboard.Offer {
	list := []wlclipboard.Offer{
		{MimeType: MimeGnomeCopiedFiles, Data: GnomePayload(paths, cut)},
		{MimeType: MimeUriList, Data: UriList(paths)},
	}
	if cut {
		list = append(list, wlclipboard.Offer{MimeType: MimeKdeCutSelection, Data: []byte("1")})
	}
	return append(list, wlclipboard.ExpandOffers([]byte(plain), MimeTextUTF8)...)
}

func validateNativePaths(paths []string) error {
	if len(paths) > maxNativePaths {
		return errors.New("clipboard file selection exceeds 10000 paths")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return errors.New("clipboard file selection requires absolute local paths")
		}
	}
	data, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	if len(data) > maxNativeSelection-1024 {
		return errors.New("clipboard file selection exceeds 1 MiB")
	}
	return nil
}

func (s *Service) callNative(method string, params, result any) error {
	call := s.call
	if call == nil {
		call = nativebridge.Call
	}
	return call(context.Background(), method, params, result)
}

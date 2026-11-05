package mimeapps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/nativebridge"
)

func nativeAppID(id string) bool {
	return filepath.IsAbs(id) && strings.EqualFold(filepath.Ext(id), ".app") && !strings.ContainsRune(id, 0)
}

func nativeOpenArgs(app string, paths []string) ([]string, error) {
	args := []string{}
	if app != "" {
		if !nativeAppID(app) {
			return nil, fail(files.CodeNotSupported, "openWith requires an absolute .app bundle path on macOS")
		}
		info, err := os.Stat(app)
		if err != nil {
			return nil, files.Wrap(app, err)
		}
		if !info.IsDir() {
			return nil, fail(files.CodeInvalid, "application is not a bundle: %s", app)
		}
		args = append(args, "-a", app)
	}
	args = append(args, "--")
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return nil, fail(files.CodeInvalid, "open requires absolute local paths")
		}
		if _, err := os.Stat(path); err != nil {
			return nil, files.Wrap(path, err)
		}
		args = append(args, path)
	}
	return args, nil
}

func (s *Service) openNative(ctx context.Context, app string, paths []string) error {
	args, err := nativeOpenArgs(app, paths)
	if err != nil {
		return err
	}
	if output, err := exec.CommandContext(ctx, "/usr/bin/open", args...).CombinedOutput(); err != nil {
		if detail := strings.TrimSpace(string(output)); detail != "" {
			return fail(files.CodeIO, "open: %s", detail)
		}
		return fail(files.CodeIO, "open: %v", err)
	}
	var entry *Entry
	if app != "" {
		entry = s.EntryByID(app)
	}
	s.noteOpened(entry, paths, nil)
	return nil
}

func nativeTerminalArgs(dir, preferred string) ([]string, error) {
	switch preferred {
	case "", "Terminal", "Terminal.app":
		preferred = "/System/Applications/Utilities/Terminal.app"
	default:
		if _, err := nativeOpenArgs(preferred, nil); err != nil {
			return nil, err
		}
	}
	if err := nativeTerminalDirectory(dir); err != nil {
		return nil, err
	}
	if options := nativeTerminalOptions(preferred, dir); len(options) > 0 {
		return append([]string{"-n", "-a", preferred, "--args"}, options...), nil
	}
	return []string{"-a", preferred, "--", dir}, nil
}

func nativeTerminalOptions(command, dir string) []string {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(command)), ".app") {
	case "alacritty", "ghostty":
		return []string{"--working-directory=" + dir}
	case "kitty":
		return []string{"--directory", dir}
	case "wezterm":
		return []string{"start", "--cwd", dir}
	default:
		return nil
	}
}

func nativeTerminalDirectory(dir string) error {
	if !filepath.IsAbs(dir) || strings.ContainsRune(dir, 0) {
		return fail(files.CodeInvalid, "terminal requires an absolute directory")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return files.Wrap(dir, err)
	}
	if !info.IsDir() {
		return fail(files.CodeNotDir, "terminal path is not a directory")
	}
	return nil
}

func (s *Service) openNativeTerminal(dir, preferred string) error {
	if preferred == "" {
		preferred = os.Getenv("TERMINAL")
	}
	if preferred != "" && preferred != "Terminal" && preferred != "Terminal.app" && !nativeAppID(preferred) {
		if err := nativeTerminalDirectory(dir); err != nil {
			return err
		}
		terminal, ok := lookupTerminal(preferred)
		if !ok {
			return fail(files.CodeNotSupported, "terminal is not installed: %s", preferred)
		}
		return spawn(append([]string{terminal.command}, nativeTerminalOptions(terminal.command, dir)...), dir, "", false)
	}
	args, err := nativeTerminalArgs(dir, preferred)
	if err != nil {
		return err
	}
	if output, err := exec.Command("/usr/bin/open", args...).CombinedOutput(); err != nil {
		return fail(files.CodeIO, "open terminal: %s: %v", strings.TrimSpace(string(output)), err)
	}
	return nil
}

type nativeAppCatalog struct {
	Apps         []*Entry            `json:"apps"`
	Associations map[string][]*Entry `json:"associations"`
	Defaults     map[string]string   `json:"defaults"`
}

func (s *Service) callNative(ctx context.Context, method string, params, result any) error {
	call := s.call
	if call == nil {
		call = nativebridge.Call
	}
	err := call(ctx, method, params, result)
	var nativeErr *nativebridge.Error
	if errors.As(err, &nativeErr) {
		return &files.Error{Code: files.Code(nativeErr.Code), Err: errors.New(nativeErr.Message)}
	}
	return err
}

func nativeEntries(entries []*Entry) []*Entry {
	out := entries[:0]
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry == nil || !nativeAppID(entry.ID) || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		out = append(out, entry)
	}
	return out
}

func (s *Service) nativeApps(mimes []string, all bool) (nativeAppCatalog, error) {
	var result nativeAppCatalog
	if err := s.callNative(context.Background(), "apps.query", map[string]any{"mimes": mimes, "includeAll": all}, &result); err != nil {
		return result, err
	}
	result.Apps = nativeEntries(result.Apps)
	for mimeType, entries := range result.Associations {
		result.Associations[mimeType] = nativeEntries(entries)
	}
	for mimeType, id := range result.Defaults {
		if !containsID(result.Associations[mimeType], id) {
			delete(result.Defaults, mimeType)
		}
	}
	return result, nil
}

func (s *Service) setNativeDefault(mimeType, id string) error {
	if !safeIniField(mimeType) || strings.ContainsRune(mimeType, 0) {
		return fail(files.CodeInvalid, "invalid mime type")
	}
	if _, err := nativeOpenArgs(id, nil); err != nil {
		return err
	}
	return s.callNative(context.Background(), "apps.setDefault", map[string]any{"mime": mimeType, "id": id}, nil)
}

func (s *Service) nativeSections(mimeTypes []string) (Sections, error) {
	types := s.sectionTypes(mimeTypes)
	out := Sections{MimeTypes: types}
	if len(types) == 0 {
		return out, nil
	}
	catalog, err := s.nativeApps(types, true)
	if err != nil {
		return out, err
	}
	if len(types) == 1 {
		out.DefaultID = catalog.Defaults[types[0]]
	}
	entries := make(map[string]*Entry, len(catalog.Apps))
	for _, entry := range catalog.Apps {
		entries[entry.ID] = entry
	}
	for _, associated := range catalog.Associations {
		for _, entry := range associated {
			entries[entry.ID] = entry
		}
	}
	lookup := func(id string) *Entry { return entries[id] }
	recommended := catalog.Associations[types[0]]
	recent := s.recentFor(types[0], lookup)
	for _, mimeType := range types[1:] {
		recommended = intersectEntries(recommended, catalog.Associations[mimeType])
		recent = intersectEntries(recent, s.recentFor(mimeType, lookup))
	}
	taken := map[string]bool{}
	for _, entry := range recent {
		if taken[entry.ID] || entry.ID == out.DefaultID || len(out.Recent) == recentLimit {
			continue
		}
		taken[entry.ID] = true
		out.Recent = append(out.Recent, entry)
	}
	out.Recommended = takeEntries(recommended, taken)
	out.Other = takeEntries(catalog.Apps, taken)
	return out, nil
}

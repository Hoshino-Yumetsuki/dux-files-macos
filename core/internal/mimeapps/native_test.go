package mimeapps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeOpenArgumentsValidatePathsWithoutLaunchingApps(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "- 雪 a; $(touch injected).txt")
	require.NoError(t, os.WriteFile(file, []byte("hello"), 0o600))
	app := filepath.Join(root, "Editor.app")
	require.NoError(t, os.Mkdir(app, 0o700))

	args, err := nativeOpenArgs("", []string{file})
	require.NoError(t, err)
	assert.Equal(t, []string{"--", file}, args)
	args, err = nativeOpenArgs(app, []string{file})
	require.NoError(t, err)
	assert.Equal(t, []string{"-a", app, "--", file}, args)

	for _, path := range []string{"-n", "relative.txt", root + "\x00"} {
		_, err := nativeOpenArgs("", []string{path})
		assert.Equal(t, files.CodeInvalid, files.CodeOf(err))
	}
	_, err = nativeOpenArgs("editor.desktop", []string{file})
	assert.Equal(t, files.CodeNotSupported, files.CodeOf(err))
	_, err = nativeOpenArgs("", []string{filepath.Join(root, "missing")})
	assert.Equal(t, files.CodeNotFound, files.CodeOf(err))
}

func TestNativeTerminalArgumentsRequireADirectory(t *testing.T) {
	root := t.TempDir()
	_, err := nativeTerminalArgs("-e", "")
	assert.Equal(t, files.CodeInvalid, files.CodeOf(err))
	_, err = nativeTerminalArgs(root, "sh -c injected")
	assert.Equal(t, files.CodeNotSupported, files.CodeOf(err))
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = nativeTerminalArgs(file, "Terminal.app")
	assert.Equal(t, files.CodeNotDir, files.CodeOf(err))
}

func TestNativeConfiguredTerminalInheritsDirectory(t *testing.T) {
	svc, root := fixture(t)
	svc.native = true
	marker := filepath.Join(root, "cwd")
	terminal := writeFile(t, filepath.Join(root, "custom terminal"), []byte("#!/bin/sh\npwd > \"$DFILES_TERMINAL_MARKER\"\n"))
	require.NoError(t, os.Chmod(terminal, 0o700))
	t.Setenv("DFILES_TERMINAL_MARKER", marker)
	t.Setenv("TERMINAL", terminal)
	directory := filepath.Join(root, "directory 雪")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, svc.OpenTerminal(directory, ""))
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && len(data) > 0 && filepath.Clean(string(data[:len(data)-1])) == directory
	}, waitFor, waitTick)
}

func nativeResult(t *testing.T, result, value any) error {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return json.Unmarshal(data, result)
}

func TestNativeMimeAndRecordedApplications(t *testing.T) {
	svc, root := fixture(t)
	svc.native = true
	path := writeFile(t, filepath.Join(root, "native.file"), []byte("hello"))
	app := &Entry{ID: filepath.Join(root, "Editor; 雪=100%+.app"), Name: "Editor"}
	failMime := false
	svc.call = func(_ context.Context, method string, params, result any) error {
		switch method {
		case "apps.mime":
			assert.Equal(t, path, params.(map[string]any)["path"])
			if failMime {
				return errors.New("native lookup unavailable")
			}
			return nativeResult(t, result, map[string]string{"mime": "text/markdown"})
		case "apps.entry":
			assert.True(t, nativeAppID(params.(map[string]any)["id"].(string)))
			if params.(map[string]any)["id"] != app.ID {
				return nativeResult(t, result, nil)
			}
			return nativeResult(t, result, app)
		default:
			t.Errorf("unexpected native call %q", method)
			return errors.New("unexpected native method")
		}
	}

	mime, err := svc.MimeOf(path)
	require.NoError(t, err)
	assert.Equal(t, "text/markdown", mime)
	require.NoError(t, svc.RecordUseForPaths([]string{path, path}, app.ID))
	assert.Equal(t, []*Entry{app}, svc.RecentFor("text/markdown"))
	data, err := os.ReadFile(writePath())
	require.NoError(t, err)
	assert.NotContains(t, string(data), app.ID)

	failMime = true
	mime, err = svc.MimeOf(path)
	require.NoError(t, err)
	assert.Equal(t, "text/plain", mime)
	mime, err = svc.MimeOf(root)
	require.NoError(t, err)
	assert.Equal(t, "inode/directory", mime)
}

func TestNativeSectionsAndDefaultIPC(t *testing.T) {
	svc, root := fixture(t)
	svc.native = true
	app := &Entry{ID: filepath.Join(root, "Viewer.app"), Name: "Viewer"}
	recent := &Entry{ID: filepath.Join(root, "Editor.app"), Name: "Editor"}
	other := &Entry{ID: filepath.Join(root, "Other.app"), Name: "Other"}
	require.NoError(t, os.Mkdir(app.ID, 0o700))
	var requested []string
	var setMime, setID string
	queryError := false
	svc.call = func(_ context.Context, method string, params, result any) error {
		switch method {
		case "apps.entry":
			assert.True(t, nativeAppID(params.(map[string]any)["id"].(string)))
			if params.(map[string]any)["id"] != recent.ID {
				return nativeResult(t, result, nil)
			}
			return nativeResult(t, result, recent)
		case "apps.query":
			if queryError {
				return &nativebridge.Error{Code: "EACCES", Message: "catalog denied"}
			}
			requested = params.(map[string]any)["mimes"].([]string)
			return nativeResult(t, result, nativeAppCatalog{
				Apps: []*Entry{app, recent, other},
				Associations: map[string][]*Entry{
					"image/png": {app, recent}, "text/plain": {recent},
				},
				Defaults: map[string]string{"image/png": app.ID},
			})
		case "apps.setDefault":
			setMime = params.(map[string]any)["mime"].(string)
			setID = params.(map[string]any)["id"].(string)
			return nil
		default:
			t.Errorf("unexpected native call %q", method)
			return errors.New("unexpected native method")
		}
	}
	require.NoError(t, svc.RecordUse("image/png", recent.ID))
	require.NoError(t, svc.RecordUse("text/plain", recent.ID))
	result := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.apps.sections", Params: map[string]any{
		"mimes": []any{"image/x-png", "image/png; charset=utf-8"},
	}}))
	assert.Equal(t, []string{"image/png"}, requested)
	assert.Equal(t, false, result["mixed"])
	assert.Equal(t, app.ID, result["defaultId"])
	assert.Equal(t, recent.ID, result["recent"].([]any)[0].(map[string]any)["id"])
	assert.Equal(t, app.ID, result["recommended"].([]any)[0].(map[string]any)["id"])
	assert.Equal(t, other.ID, result["other"].([]any)[0].(map[string]any)["id"])

	sections := svc.Sections([]string{"image/png", "text/plain"})
	assert.Empty(t, sections.DefaultID)
	assert.Equal(t, []*Entry{recent}, sections.Recent)
	assert.Empty(t, sections.Recommended)

	result = resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.apps.setDefault", Params: map[string]any{
		"mime": "image/png", "desktopId": app.ID,
	}}))
	assert.Equal(t, app.ID, result["desktopId"])
	assert.Equal(t, "image/png", setMime)
	assert.Equal(t, app.ID, setID)

	queryError = true
	out := call(t, svc, ipc.Request{ID: 3, Method: "files.apps.forMime", Params: map[string]any{"mime": "image/png"}})
	assert.Equal(t, "EACCES", out["code"])
}

func TestNativeCatalogRejectsInvalidApplicationIDs(t *testing.T) {
	svc, root := fixture(t)
	svc.native = true
	valid := &Entry{ID: filepath.Join(root, "Valid.app"), Name: "Valid"}
	calls := 0
	svc.call = func(_ context.Context, method string, _ any, result any) error {
		calls++
		if method == "apps.entry" {
			return nativeResult(t, result, valid)
		}
		return nativeResult(t, result, nativeAppCatalog{
			Apps:         []*Entry{nil, {ID: "editor.desktop"}, valid, valid},
			Associations: map[string][]*Entry{"text/plain": {nil, {ID: "editor.desktop"}, valid, valid}},
			Defaults:     map[string]string{"text/plain": "editor.desktop"},
		})
	}
	assert.Nil(t, svc.EntryByID("editor.desktop"))
	assert.Zero(t, calls)
	assert.Nil(t, svc.EntryByID(filepath.Join(root, "Different.app")))
	catalog, err := svc.nativeApps([]string{"text/plain"}, true)
	require.NoError(t, err)
	assert.Equal(t, []*Entry{valid}, catalog.Apps)
	assert.Equal(t, []*Entry{valid}, catalog.Associations["text/plain"])
	assert.Empty(t, catalog.Defaults)
}

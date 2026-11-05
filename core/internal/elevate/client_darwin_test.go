package elevate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-files/core/internal/fileops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinElevationHasNoRoute(t *testing.T) {
	c := NewClient()
	assert.Empty(t, c.Route())
	var unavailable *fileops.Unavailable
	require.ErrorAs(t, c.Available(), &unavailable)
	assert.Equal(t, "NOROUTE", unavailable.Reason)
	_, err := c.command("remove", t.TempDir())
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, "NOROUTE", unavailable.Reason)
}

func TestDarwinElevationCannotFallBackToProcessOrLocalOperations(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original")
	created := filepath.Join(dir, "created")
	require.NoError(t, os.WriteFile(original, []byte("unchanged"), 0o600))
	before, err := os.Stat(original)
	require.NoError(t, err)
	c := &Client{pkexec: "/usr/bin/false", exe: "/usr/bin/false"}
	t.Setenv("PKEXEC_UID", "0")

	for _, test := range []struct {
		name string
		call func() error
	}{
		{"lstat", func() error { _, err := c.Lstat(original); return err }},
		{"readdir", func() error { _, err := c.ReadDir(dir); return err }},
		{"readlink", func() error { _, err := c.Readlink(original); return err }},
		{"read", func() error { _, err := c.Open(original); return err }},
		{"write", func() error { _, err := c.Create(created, 0o600); return err }},
		{"mkdir", func() error { return c.Mkdir(created, 0o700) }},
		{"mkdirall", func() error { return c.MkdirAll(created, 0o700) }},
		{"symlink", func() error { return c.Symlink(original, created) }},
		{"rename", func() error { return c.Rename(original, created, false) }},
		{"remove", func() error { return c.Remove(original) }},
		{"removeall", func() error { return c.RemoveAll(dir) }},
		{"chmod", func() error { return c.Chmod(original, 0o777, true) }},
		{"chown", func() error { return c.Lchown(original, 0, 0, true) }},
		{"utimes", func() error { return c.Chtimes(original, time.Unix(0, 0)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var unavailable *fileops.Unavailable
			require.ErrorAs(t, test.call(), &unavailable)
			assert.Equal(t, "NOROUTE", unavailable.Reason)
		})
	}

	body, err := os.ReadFile(original)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", string(body))
	after, err := os.Stat(original)
	require.NoError(t, err)
	assert.Equal(t, before.Mode(), after.Mode())
	assert.Equal(t, before.ModTime(), after.ModTime())
	_, err = os.Lstat(created)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

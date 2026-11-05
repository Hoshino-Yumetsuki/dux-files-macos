package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinXDGDefaultsPreserveExplicitEnvironment(t *testing.T) {
	keys := []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}
	for _, key := range keys {
		t.Setenv(key, "restore")
		require.NoError(t, os.Unsetenv(key))
	}
	home := t.TempDir()
	require.NoError(t, setDarwinXDGPaths(home))
	assert.Equal(t, filepath.Join(home, "Library", "Application Support"), os.Getenv("XDG_CONFIG_HOME"))
	assert.Equal(t, filepath.Join(home, "Library", "Application Support", "dfiles", "data"), os.Getenv("XDG_DATA_HOME"))
	assert.Equal(t, filepath.Join(home, "Library", "Caches"), os.Getenv("XDG_CACHE_HOME"))
	assert.Equal(t, filepath.Join(home, "Library", "Application Support", "state"), os.Getenv("XDG_STATE_HOME"))

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "custom"))
	t.Setenv("XDG_DATA_HOME", "")
	require.NoError(t, setDarwinXDGPaths(filepath.Join(home, "elsewhere")))
	assert.Equal(t, filepath.Join(home, "custom"), os.Getenv("XDG_CONFIG_HOME"))
	assert.Empty(t, os.Getenv("XDG_DATA_HOME"))
}

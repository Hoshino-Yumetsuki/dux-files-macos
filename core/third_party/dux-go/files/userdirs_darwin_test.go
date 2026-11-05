package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeUserFoldersWithoutXDGConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	for _, key := range userDirOrder {
		t.Setenv(key, "")
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads", "Movies", "Videos"} {
		require.NoError(t, os.Mkdir(filepath.Join(home, name), 0o755))
	}
	var keys, paths []string
	for _, dir := range listUserDirs() {
		keys = append(keys, dir.Key)
		paths = append(paths, dir.Path)
	}
	assert.Equal(t, []string{"home", "desktop", "documents", "download", "videos"}, keys)
	assert.Contains(t, paths, filepath.Join(home, "Movies"))
	assert.NotContains(t, paths, filepath.Join(home, "Videos"))
}

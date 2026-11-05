package helper

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type forbiddenIO struct{}

func (forbiddenIO) Read([]byte) (int, error)  { panic("unsupported helper read input") }
func (forbiddenIO) Write([]byte) (int, error) { panic("unsupported helper wrote output") }

func TestDarwinHelperCannotExecutePrivilegedOperations(t *testing.T) {
	t.Setenv("PKEXEC_UID", "0")
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	created := filepath.Join(dir, "created")
	require.NoError(t, os.WriteFile(path, []byte("unchanged"), 0o600))
	before, err := os.Stat(path)
	require.NoError(t, err)

	for _, args := range [][]string{
		nil,
		{"unknown"},
		{OpLstat, path},
		{OpReadDir, dir},
		{OpReadlink, path},
		{OpRead, path},
		{OpWrite, created, "600"},
		{OpMkdir, created, "700"},
		{OpMkdirAll, created, "700"},
		{OpSymlink, path, created},
		{OpRename, path, created},
		{OpRemove, path},
		{OpRemoveAll, dir},
		{OpChmod, path, "777", FlagRecursive},
		{OpChown, path, "0", "0", FlagRecursive},
		{OpUtimes, path, "0"},
	} {
		name := "missing operation"
		if len(args) > 0 {
			name = args[0]
		}
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, files.CodeNotSupported, CodeOf(Run(args, forbiddenIO{}, forbiddenIO{})))
			var stderr bytes.Buffer
			assert.Equal(t, ExitRefused, Main(args, forbiddenIO{}, forbiddenIO{}, &stderr))
			var failure Failure
			require.NoError(t, json.Unmarshal(stderr.Bytes(), &failure))
			assert.Equal(t, string(files.CodeNotSupported), failure.Code)
			assert.NotEmpty(t, failure.Error)
		})
	}

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", string(body))
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.Mode(), after.Mode())
	assert.Equal(t, before.ModTime(), after.ModTime())
	_, err = os.Lstat(created)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

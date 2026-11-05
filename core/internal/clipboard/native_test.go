package clipboard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeClipboardRoundTripAndFailures(t *testing.T) {
	paths := []string{"/tmp/雪 #100%.txt", "/tmp/other.txt"}
	calls := []string{}
	failure := false
	image := []byte("png data")
	mime := "image/png"
	foreign := false
	svc := &Service{native: true}
	svc.call = func(_ context.Context, method string, params, result any) error {
		calls = append(calls, method)
		if failure {
			return errors.New("native helper failed")
		}
		var value any
		switch method {
		case "clipboard.copy":
			assert.Equal(t, paths, params.(map[string]any)["paths"])
			assert.Equal(t, true, params.(map[string]any)["cut"])
			return nil
		case "clipboard.paths":
			source := "dfiles"
			if foreign {
				source = MimeUriList
			}
			value = map[string]any{"paths": paths, "cut": true, "source": source}
		case "clipboard.image":
			value = map[string]any{"data": image, "mime": mime}
		case "clipboard.clear":
			return nil
		default:
			t.Errorf("unexpected native method %q", method)
			return errors.New("unexpected native method")
		}
		data, err := json.Marshal(value)
		require.NoError(t, err)
		return json.Unmarshal(data, result)
	}

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go svc.Handle(ipc.NewConnWriter(server), ipc.Request{ID: 1, Method: "clipboard.copy", Params: map[string]any{"paths": []any{paths[0], paths[1]}, "cut": true}})
	line, err := bufio.NewReader(client).ReadBytes('\n')
	require.NoError(t, err)
	var response struct {
		Result struct {
			Paths []string `json:"paths"`
			Cut   bool     `json:"cut"`
		} `json:"result"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(line, &response))
	assert.Empty(t, response.Error)
	assert.Equal(t, paths, response.Result.Paths)
	assert.True(t, response.Result.Cut)

	got, cut, source := svc.Paths()
	assert.Equal(t, paths, got)
	assert.True(t, cut)
	assert.Equal(t, "dfiles", source)
	foreign = true
	_, cut, source = svc.Paths()
	assert.False(t, cut)
	assert.Equal(t, MimeUriList, source)

	data, gotMime, ok := svc.Image()
	require.True(t, ok)
	assert.Equal(t, image, data)
	assert.Equal(t, mime, gotMime)
	mime = "text/plain"
	_, _, ok = svc.Image()
	assert.False(t, ok)
	require.NoError(t, svc.Clear())
	assert.Equal(t, []string{"clipboard.copy", "clipboard.paths", "clipboard.paths", "clipboard.image", "clipboard.image", "clipboard.clear"}, calls)

	failure = true
	require.Error(t, svc.Copy(paths, false))
	require.Error(t, svc.Clear())
	got, cut, source = svc.Paths()
	assert.Empty(t, got)
	assert.False(t, cut)
	assert.Empty(t, source)
	_, _, ok = svc.Image()
	assert.False(t, ok)
}

func TestNativeClipboardRejectsOversizedAndInvalidSelections(t *testing.T) {
	called := false
	svc := &Service{native: true, call: func(context.Context, string, any, any) error {
		called = true
		return nil
	}}
	for _, paths := range [][]string{nil, {"relative"}, {"/tmp/bad\x00"}, {"/" + strings.Repeat("x", maxNativeSelection)}} {
		require.Error(t, svc.Copy(paths, false))
		assert.False(t, called)
	}
	paths := make([]string, maxNativePaths+1)
	for i := range paths {
		paths[i] = "/tmp/file"
	}
	require.Error(t, svc.Copy(paths, false))
	assert.False(t, called)

	svc.call = func(_ context.Context, method string, _, result any) error {
		var value any = map[string]any{"paths": []string{"relative"}, "cut": true, "source": "dfiles"}
		if method == "clipboard.image" {
			value = map[string]any{"data": make([]byte, maxNativeImage+1), "mime": "image/png"}
		}
		data, err := json.Marshal(value)
		require.NoError(t, err)
		return json.Unmarshal(data, result)
	}
	got, cut, source := svc.Paths()
	assert.Empty(t, got)
	assert.False(t, cut)
	assert.Empty(t, source)
	_, _, ok := svc.Image()
	assert.False(t, ok)
}

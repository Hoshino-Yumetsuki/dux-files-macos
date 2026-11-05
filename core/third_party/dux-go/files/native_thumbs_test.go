package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeThumbnailGenerationAndCache(t *testing.T) {
	thumbs := NewThumbs(t.TempDir())
	calls := 0
	thumbs.native = func(_ context.Context, method string, params, result any) error {
		calls++
		assert.Equal(t, "media.thumbnail", method)
		args := params.(map[string]any)
		assert.Equal(t, 128, args["size"])
		img := image.NewNRGBA(image.Rect(0, 0, 32, 16))
		img.Set(10, 8, color.NRGBA{R: 255, A: 255})
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, img))
		result.(*struct {
			Data string `json:"data"`
		}).Data = base64.StdEncoding.EncodeToString(data.Bytes())
		return nil
	}
	entry := Entry{Path: filepath.Join(t.TempDir(), "clip.pdf"), Mime: "application/pdf", MtimeMs: 12345, Size: 64}
	require.True(t, thumbs.Supports(entry.Mime))
	require.True(t, thumbs.Supports("video/mp4"))
	require.True(t, thumbs.Supports("audio/mpeg"))
	require.False(t, thumbs.Supports("text/plain"))
	require.False(t, thumbs.Supports("image/svg+xml"))
	path, err := thumbs.Generate(entry, "normal")
	require.NoError(t, err)
	require.FileExists(t, path)
	cached, err := thumbs.Generate(entry, "normal")
	require.NoError(t, err)
	assert.Equal(t, path, cached)
	assert.Equal(t, 1, calls)
}

func TestDatalessNativeThumbnailFailureDoesNotPoisonCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.png")
	require.NoError(t, os.WriteFile(path, []byte("fixture"), 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	entry := Entry{Path: path, Mime: "image/png", MtimeMs: info.ModTime().UnixMilli(), Dataless: true}
	thumbs := NewThumbs(t.TempDir())
	calls := 0
	thumbs.native = func(_ context.Context, _ string, _, result any) error {
		calls++
		if calls == 1 {
			return errors.New("provider has no thumbnail")
		}
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8))))
		result.(*struct {
			Data string `json:"data"`
		}).Data = base64.StdEncoding.EncodeToString(data.Bytes())
		return nil
	}
	_, err = thumbs.Generate(entry, "normal")
	require.Error(t, err)
	require.False(t, thumbs.Failed(path, entry.MtimeMs))
	entry.Dataless = false
	cached, err := thumbs.Generate(entry, "normal")
	require.NoError(t, err)
	require.FileExists(t, cached)
	assert.Equal(t, 2, calls)
}

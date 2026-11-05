package mimeapps

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNativeServiceDetectorWithoutMimeDatabaseOrHelper(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_DATA_DIRS", filepath.Join(root, "share"))
	t.Setenv("DFILES_NATIVE_HELPER", filepath.Join(root, "missing-helper"))
	svc := NewService()
	svc.call = func(context.Context, string, any, any) error {
		t.Error("file listing MIME detection must not spawn native helpers")
		return nil
	}
	detector := svc.Detector()
	for name, want := range map[string]string{
		"movie.MP4": "video/mp4", "screen.mov": "video/quicktime", "movie.m4v": "video/mp4",
		"movie.mkv": "video/x-matroska", "clip.webm": "video/webm", "doc.pdf": "application/pdf",
		"image.PNG": "image/png", "image.heic": "image/heic", "image.heif": "image/heif",
		"song.mp3": "audio/mpeg", "note.txt": "text/plain",
	} {
		assert.Equal(t, want, detector.FromName(name), name)
	}
	for _, test := range []struct {
		name, body, want string
	}{
		{"pdf", "%PDF-1.7\n", "application/pdf"},
		{"png", "\x89PNG\r\n\x1a\n", "image/png"},
		{"video", "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom", "video/mp4"},
		{"text", "héllo world\n", "text/plain"},
		{"blob", "\x00\x01\x02\x03", "application/octet-stream"},
	} {
		path := writeFile(t, filepath.Join(root, test.name), []byte(test.body))
		assert.Equal(t, test.want, detector.Sniff(path), test.name)
		assert.Equal(t, test.want, detector.Detect(path, false), test.name)
	}
	assert.Equal(t, TypeDirectory, detector.Detect(root, true))
	assert.Empty(t, detector.FromName("unknown.dux-unknown"))
	assert.Empty(t, detector.Sniff(filepath.Join(root, "missing")))
	assert.Empty(t, NewServiceForDirs(nil).Detector().FromName("movie.mp4"))
	assert.Equal(t, TypeText, detector.Sniff(writeFile(t, filepath.Join(root, "long-text"), []byte(strings.Repeat("a", 512)))))
}

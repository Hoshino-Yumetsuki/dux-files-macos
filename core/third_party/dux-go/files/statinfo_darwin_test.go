package files

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type datalessInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (i datalessInfo) Sys() any { return &i.stat }

func TestEntryLocalityFromFileMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("local content"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata := datalessInfo{FileInfo: info, stat: *info.Sys().(*syscall.Stat_t)}
	builder := newBuilder(nil, nil)
	entry := Entry{Name: info.Name(), Path: path}
	builder.fill(&entry, metadata, path)
	if entry.Dataless {
		t.Fatal("ordinary file reported dataless")
	}
	metadata.stat.Flags |= 0x40000000 // SF_DATALESS
	builder.fill(&entry, metadata, path)
	if !entry.Dataless || entry.Mime != "image/png" {
		t.Fatalf("placeholder metadata: dataless=%v mime=%q", entry.Dataless, entry.Mime)
	}
	metadata.stat.Flags &^= 0x40000000
	builder.fill(&entry, metadata, path)
	if entry.Dataless {
		t.Fatal("materialized file still reported dataless")
	}
}

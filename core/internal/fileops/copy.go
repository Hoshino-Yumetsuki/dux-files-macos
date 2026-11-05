package fileops

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

var copyChunk = 256 << 10

const tempNameTail = 64

type outcome struct {
	dst      string
	undoable bool
	dir      bool
}
type pendingRemoval struct {
	path string
	info os.FileInfo
}

func (r *runner) transferAll(move bool) error {
	return r.transferPairs(r.targets(), move)
}

func (r *runner) transferPairs(pairs []pair, move bool) error {
	var needScan []string
	for _, p := range pairs {
		if move && sameDevice(p.src, filepath.Dir(p.dst)) {
			continue
		}
		needScan = append(needScan, p.src)
	}

	switch len(needScan) {
	case 0:
		r.setTotals(len(pairs), 0, false)
	default:
		count, bytes, complete := r.scan(needScan)
		r.setTotals(count+len(pairs)-len(needScan), bytes, !complete)
	}

	for _, p := range pairs {
		if r.job.closed() {
			return nil
		}
		if !r.guarded(p.src) {
			return errors.New("changed since the operation it would replay: " + p.src)
		}
		done, err := r.coordinatedTransfer(p.src, p.dst, move)
		switch {
		case errors.Is(err, errSkipped):
			continue
		case errors.Is(err, context.Canceled):
			return nil
		case err != nil:
			return err
		case done.undoable:
			r.remember(inverseOf(move), done.dst, p.src)
		}
		if r.job.spec.kind == KindDuplicate && done.dst != "" {
			r.noteCreated(done.dst, done.dir)
		}
	}
	return nil
}

func (r *runner) transfer(src, dst string, move bool) (outcome, error) {
	var info os.FileInfo
	if err := r.retry(src, func(fs FS) error {
		var statErr error
		info, statErr = fs.Lstat(src)
		return statErr
	}); err != nil {
		if r.pending != nil {
			r.pendingInvalid = true
		}
		return outcome{}, err
	}

	res := r.resolve(src, dst, info, move)
	switch {
	case res.abort:
		return outcome{}, context.Canceled
	case res.skip:
		return outcome{}, errSkipped
	}
	dst = res.dst

	undoable := !res.merge && !res.replaced
	if move && r.pending == nil && !res.merge && r.renameInto(src, dst, info) {
		return outcome{dst: dst, undoable: undoable, dir: info.IsDir()}, nil
	}
	if err := r.clearTarget(dst, res, info.IsDir()); err != nil {
		if r.pending != nil {
			r.pendingInvalid = true
		}
		return outcome{}, err
	}
	rootMove := move && info.IsDir() && r.pending == nil
	if rootMove {
		pending := []pendingRemoval{}
		r.pending = &pending
		r.pendingInvalid = false
		defer func() { r.pending = nil }()
	}
	deferDelete := move && r.pending != nil
	var err error
	switch {
	case info.IsDir():
		err = r.transferDir(src, dst, move, res.merge)
	case info.Mode()&fs.ModeSymlink != 0:
		err = r.transferLink(src, dst, move && !deferDelete, !res.replaced)
	case info.Mode().IsRegular():
		err = r.transferFile(src, dst, info, move && !deferDelete, !res.replaced)
	default:
		r.skip()
		return outcome{}, errSkipped
	}
	if err != nil {
		if deferDelete {
			r.pendingInvalid = true
		}
		return outcome{}, err
	}
	if deferDelete {
		*r.pending = append(*r.pending, pendingRemoval{path: src, info: info})
	}
	if rootMove {
		if r.pendingInvalid {
			return outcome{dst: dst, dir: true}, nil
		}
		for _, item := range *r.pending {
			current, statErr := os.Lstat(item.path)
			if statErr != nil || !os.SameFile(item.info, current) || (!item.info.IsDir() && (item.info.Size() != current.Size() || !item.info.ModTime().Equal(current.ModTime()))) {
				return outcome{}, errors.New("source changed during move: " + item.path)
			}
		}
		for _, item := range *r.pending {
			if r.job.closed() {
				return outcome{}, context.Canceled
			}
			if item.info.IsDir() {
				var entries []os.DirEntry
				if err := r.retry(item.path, func(fs FS) error { var err error; entries, err = fs.ReadDir(item.path); return err }); err != nil {
					return outcome{}, err
				}
				if len(entries) != 0 {
					undoable = false
					continue
				}
			}
			if err := r.retry(item.path, func(fs FS) error { return fs.Remove(item.path) }); err != nil {
				return outcome{}, err
			}
		}
	}
	return outcome{dst: dst, undoable: undoable, dir: info.IsDir()}, nil
}

func (r *runner) renameInto(src, dst string, info os.FileInfo) bool {
	if !sameDevice(src, filepath.Dir(dst)) {
		return false
	}
	_, statErr := os.Lstat(dst)
	switch {
	case statErr == nil && info.IsDir():
		return false
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		return false
	}
	if err := os.Rename(src, dst); err != nil {
		r.scanLate(src)
		return false
	}

	counted := r.scanned[src]
	if counted.files == 0 {
		counted.files = 1
	}
	r.advance(dst, counted.files, counted.bytes)
	return true
}

func (r *runner) scanLate(src string) {
	if _, known := r.scanned[src]; known {
		return
	}
	count, bytes, complete := r.scan([]string{src})
	r.job.mu.Lock()
	r.job.progress.FilesTotal += count - 1
	r.job.progress.BytesTotal += bytes
	r.job.progress.Estimating = r.job.progress.Estimating || !complete
	r.job.mu.Unlock()
	r.estimating = r.estimating || !complete
}

func (r *runner) clearTarget(dst string, res resolution, srcIsDir bool) error {
	if res.merge {
		return nil
	}
	target, err := os.Lstat(dst)
	if err != nil {
		return nil
	}
	if !target.IsDir() && !srcIsDir {
		return nil
	}
	return r.retry(dst, func(fs FS) error { return fs.RemoveAll(dst) })
}

func (r *runner) transferDir(src, dst string, move, merge bool) error {
	var info os.FileInfo
	if err := r.retry(src, func(fs FS) error {
		var statErr error
		info, statErr = fs.Lstat(src)
		return statErr
	}); err != nil {
		return err
	}

	dstFS := local
	if err := r.retry(dst, func(fs FS) error {
		dstFS = fs
		return fs.MkdirAll(dst, info.Mode().Perm()|0o700)
	}); err != nil {
		return err
	}

	var children []os.DirEntry
	if err := r.retry(src, func(fs FS) error {
		var readErr error
		children, readErr = fs.ReadDir(src)
		return readErr
	}); err != nil {
		return err
	}
	if r.estimating {
		r.growTotals(len(children))
	}

	for _, child := range children {
		if r.job.closed() {
			return context.Canceled
		}
		_, err := r.transfer(filepath.Join(src, child.Name()), filepath.Join(dst, child.Name()), move)
		switch {
		case err == nil, errors.Is(err, errSkipped):
			continue
		default:
			return err
		}
	}

	if err := r.retryLocal(dst, func() error { return r.preserve(src, dst, info, dstFS) }); err != nil {
		return err
	}
	r.advance(dst, 1, 0)
	return nil
}

func (r *runner) transferLink(src, dst string, move, noReplace bool) error {
	var target string
	if err := r.retry(src, func(fs FS) error {
		var readErr error
		target, readErr = fs.Readlink(src)
		return readErr
	}); err != nil {
		return err
	}

	tmp := r.tempPath(dst)
	made := &creation{path: tmp}
	if err := r.retry(dst, func(fs FS) error {
		made.discard()
		if err := fs.Symlink(target, tmp); err != nil {
			return err
		}
		made.fs = fs
		return nil
	}); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		made.discard()
		return err
	}
	if err := r.retryLocal(dst, func() error { return r.preserve(src, tmp, info, made.fs) }); err != nil {
		made.discard()
		return err
	}
	if err := r.commit(tmp, dst, made, noReplace); err != nil {
		return err
	}

	r.advance(dst, 1, 0)
	return r.dropSource(src, move)
}

func (r *runner) transferFile(src, dst string, info os.FileInfo, move, noReplace bool) error {
	tmp := r.tempPath(dst)
	made := &creation{path: tmp}
	if err := r.retryLocal(src, func() error {
		made.discard()
		return r.copyBytes(src, tmp, info, made)
	}); err != nil {
		made.discard()
		return err
	}

	if err := r.retryLocal(dst, func() error { return r.preserve(src, tmp, info, made.fs) }); err != nil {
		made.discard()
		return err
	}
	if err := r.commit(tmp, dst, made, noReplace); err != nil {
		return err
	}

	r.advance(dst, 1, 0)
	return r.dropSource(src, move)
}

func (r *runner) commit(tmp, dst string, made *creation, noReplace bool) error {
	if err := r.retry(dst, func(fs FS) error { return fs.Rename(tmp, dst, noReplace) }); err != nil {
		made.discard()
		return err
	}
	return nil
}

func (r *runner) dropSource(src string, move bool) error {
	if !move {
		return nil
	}
	return r.retry(src, func(fs FS) error { return fs.Remove(src) })
}

func (r *runner) copyBytes(src, tmp string, info os.FileInfo, made *creation) error {
	var in io.ReadCloser
	srcFS := local
	if err := r.retry(src, func(fs FS) error {
		opened, err := fs.Open(src)
		in = opened
		srcFS = fs
		return err
	}); err != nil {
		return err
	}
	defer in.Close()
	if _, ok := srcFS.(localFS); ok {
		cloned, err := cloneFile(src, tmp)
		if err != nil {
			return err
		}
		if cloned {
			made.fs = local
			if r.job.closed() {
				made.discard()
				return context.Canceled
			}
			r.advance(src, 0, info.Size())
			return nil
		}
	}

	var out Writer
	if err := r.retry(tmp, func(fs FS) error {
		created, err := fs.Create(tmp, info.Mode().Perm())
		if err != nil {
			return err
		}
		out = created
		made.fs = fs
		return nil
	}); err != nil {
		return err
	}

	buf := make([]byte, copyChunk)
	for {
		if r.job.closed() {
			made.abort(out)
			return context.Canceled
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				made.abort(out)
				return writeErr
			}
			r.advance(src, 0, int64(n))
		}
		if readErr == io.EOF {
			if err := out.Close(); err != nil {
				made.discard()
				return err
			}
			return nil
		}
		if readErr != nil {
			made.abort(out)
			return readErr
		}
	}
}

func (r *runner) preserve(src, dst string, info os.FileInfo, through FS) error {
	if through == nil {
		through = local
	}
	err := preserveMetadata(r.job.ctx, src, dst, info, through)
	if err != nil && !metadataStrict {
		r.warn(dst, err)
		return nil
	}
	return err
}

func (r *runner) growTotals(children int) {
	r.job.mu.Lock()
	r.job.progress.FilesTotal += children
	r.job.mu.Unlock()
}

func (r *runner) tempPath(dst string) string {
	return filepath.Join(filepath.Dir(dst), ".dfiles-tmp-"+r.job.id+r.job.nextID("-t")+"-"+truncateName(filepath.Base(dst), tempNameTail))
}

func truncateName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

func inverseOf(move bool) Kind {
	if move {
		return KindMove
	}
	return KindDelete
}

func sameDevice(src, destDir string) bool {
	srcDev, ok := deviceOf(src)
	if !ok {
		return false
	}
	dstDev, ok := deviceOf(destDir)
	if !ok {
		return false
	}
	return srcDev == dstDev
}

func deviceOf(path string) (uint64, bool) {
	for {
		var st syscall.Stat_t
		if err := syscall.Lstat(path, &st); err == nil {
			return uint64(st.Dev), true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, false
		}
		path = parent
	}
}

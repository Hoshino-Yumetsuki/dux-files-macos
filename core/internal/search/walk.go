package search

import (
	"container/heap"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/duxlabs-dev/dux-go/files"
)

const (
	batchSize      = 200
	flushInterval  = 100 * time.Millisecond
	fuzzyReservoir = 2000
)

type hit struct {
	files.Entry
	Score int `json:"score"`
}

type event struct {
	SearchID         string `json:"searchId"`
	Kind             string `json:"kind"`
	Entries          []hit  `json:"entries,omitempty"`
	Count            int    `json:"count"`
	Truncated        bool   `json:"truncated,omitempty"`
	Partial          bool   `json:"partial,omitempty"`
	Cancelled        bool   `json:"cancelled,omitempty"`
	Unreadable       int    `json:"unreadable,omitempty"`
	Backend          string `json:"backend,omitempty"`
	Phase            string `json:"phase,omitempty"`
	DepthLimit       int    `json:"depthLimit,omitempty"`
	ContentAvailable bool   `json:"contentAvailable,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Error            string `json:"error,omitempty"`
	Code             string `json:"code,omitempty"`
}

type search struct {
	id, topic string
	svc       *Service
	ctx       context.Context
	cancel    context.CancelFunc
	finished  chan struct{}

	mu       sync.Mutex
	attached bool
	done     bool
	pending  []event
	stopWait func() bool
}

func newSearch(svc *Service) *search {
	ctx, cancel := context.WithCancel(context.Background())
	return &search{svc: svc, ctx: ctx, cancel: cancel, finished: make(chan struct{})}
}

func (sc *search) follow(parent context.Context) {
	sc.stopWait = context.AfterFunc(parent, func() {
		sc.cancel()
		sc.svc.unregister(sc.id)
	})
}

func (sc *search) run(root, query string, opts Options) {
	defer close(sc.finished)
	defer sc.retire()

	targets := roots(opts.Scope, root, sc.svc.mounts)
	terms := parseTerms(query)
	em := newEmitter(sc, opts.limit())
	reason := sc.svc.index.serve(sc, em, targets, query, terms, opts)
	if reason == "" {
		return
	}
	if opts.Contents {
		code := files.CodeNotSupported
		if reason == reasonError {
			code = files.CodeIO
		}
		sc.publish(event{Kind: "error", Code: string(code), Reason: reason, Error: "indexed content search is unavailable (" + reason + ")"})
		em.finish(true, 0)
		return
	}
	sc.walk(em, targets, terms, opts, reason)
}

func (sc *search) walk(em *emitter, targets []string, terms []term, opts Options, reason string) {
	sc.publish(event{Kind: "backend", Backend: "walk", Reason: reason, ContentAvailable: sc.svc.index.native != nil && sc.svc.index.enabled.Load() && reason != reasonNoIndex && reason != reasonError})
	var excludes []string
	if sc.svc.excludes != nil {
		excludes = sc.svc.excludes()
	}
	w := &walker{
		sc:        sc,
		em:        em,
		terms:     terms,
		opts:      opts,
		rules:     newIgnoreRules(excludes, opts.IncludeIgnored),
		reservoir: reservoir{limit: fuzzyReservoir},
	}
	for _, target := range targets {
		info := &rootInfo{path: target}
		if !opts.CrossDevice {
			if dev, ok := deviceOf(target); ok {
				info.device, info.sameDevice = dev, true
			}
		}
		w.queue.push(dirItem{path: target, root: info, patterns: w.rules.rootPatterns(target)})
	}

	var workers sync.WaitGroup
	for range sc.svc.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w.work()
		}()
	}
	go func() {
		w.queue.wait()
		w.queue.close()
	}()
	workers.Wait()
	w.flushReservoir()
	em.finish(false, int(w.unreadable.Load()))
}

func (sc *search) publish(ev event) {
	ev.SearchID = sc.id
	sc.mu.Lock()
	if !sc.attached && !sc.svc.attachOnOpen.Load() {
		sc.pending = append(sc.pending, ev)
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()
	sc.svc.publish(sc.topic, ev)
}

func (sc *search) attach() {
	sc.mu.Lock()
	sc.attached = true
	for _, ev := range sc.pending {
		sc.svc.publish(sc.topic, ev)
	}
	sc.pending = nil
	done := sc.done
	sc.mu.Unlock()
	if done {
		sc.drop()
	}
}

// Must run before the done event, or a client that saw the search finish could cancel a still registered id.
func (sc *search) retire() {
	sc.mu.Lock()
	if sc.done {
		sc.mu.Unlock()
		return
	}
	sc.done = true
	flushed := sc.attached || sc.svc.attachOnOpen.Load()
	sc.mu.Unlock()
	if flushed {
		sc.drop()
	}
}

func (sc *search) drop() {
	if sc.stopWait != nil {
		sc.stopWait()
	}
	sc.svc.unregister(sc.id)
}

type rootInfo struct {
	path       string
	device     uint64
	sameDevice bool
}

type walker struct {
	sc         *search
	em         *emitter
	terms      []term
	opts       Options
	rules      ignoreRules
	queue      dirQueue
	reservoir  reservoir
	unreadable atomic.Int64
}

func (w *walker) work() {
	for {
		item, ok := w.queue.pop()
		if !ok {
			return
		}
		w.readDir(item)
		w.queue.done()
	}
}

func (w *walker) readDir(item dirItem) {
	if w.sc.ctx.Err() != nil {
		return
	}
	children, err := os.ReadDir(item.path)
	if err != nil {
		w.unreadable.Add(1)
		return
	}
	patterns := w.rules.loadIf(item.path, children, item.patterns)
	for _, child := range children {
		if w.sc.ctx.Err() != nil {
			return
		}
		name := child.Name()
		if !w.opts.IncludeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(item.path, name)
		isDir := child.Type()&fs.ModeSymlink == 0 && child.IsDir()
		if isDir && w.rules.skipsDir(name, full) {
			continue
		}
		if ignored(patterns, full, isDir) {
			continue
		}
		nameLower := strings.ToLower(name)
		relLower := nameLower
		if item.relLower != "" {
			relLower = item.relLower + "/" + nameLower
		}
		if isDir && w.descends(full, item.root) {
			w.queue.push(dirItem{
				path:     full,
				relLower: relLower,
				depth:    item.depth + 1,
				root:     item.root,
				patterns: patterns,
			})
		}
		points, tier := score(nameLower, relLower, item.depth, w.terms, w.opts.Fuzzy)
		switch tier {
		case tierNone:
		case tierFuzzy:
			w.reservoir.offer(full, points)
		default:
			if !w.emit(full, points) {
				return
			}
		}
	}
}

func (w *walker) descends(dir string, root *rootInfo) bool {
	if !root.sameDevice {
		return true
	}
	dev, ok := deviceOf(dir)
	return !ok || dev == root.device
}

func (w *walker) emit(path string, points int) bool {
	entry, err := w.sc.svc.stater.Stat(path)
	if err != nil || !w.opts.accepts(entry) {
		return true
	}
	return w.em.emit(hit{Entry: entry, Score: points})
}

func (w *walker) flushReservoir() {
	for _, f := range w.reservoir.drain() {
		if w.sc.ctx.Err() != nil {
			return
		}
		if !w.emit(f.path, f.score) {
			return
		}
	}
}

type fuzzyHit struct {
	path  string
	score int
}

type fuzzyHeap []fuzzyHit

func (h fuzzyHeap) Len() int           { return len(h) }
func (h fuzzyHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h fuzzyHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *fuzzyHeap) Push(x any)        { *h = append(*h, x.(fuzzyHit)) }
func (h *fuzzyHeap) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

type reservoir struct {
	mu    sync.Mutex
	limit int
	items fuzzyHeap
}

func (r *reservoir) offer(path string, score int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case len(r.items) < r.limit:
		heap.Push(&r.items, fuzzyHit{path, score})
	case score > r.items[0].score:
		r.items[0] = fuzzyHit{path, score}
		heap.Fix(&r.items, 0)
	}
}

func (r *reservoir) drain() []fuzzyHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.items
	r.items = nil
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

type emitter struct {
	sc      *search
	limit   int
	matches chan hit
	done    chan event
}

func newEmitter(sc *search, limit int) *emitter {
	em := &emitter{sc: sc, limit: limit, matches: make(chan hit, batchSize), done: make(chan event, 1)}
	go em.collect()
	return em
}

func (em *emitter) emit(h hit) bool {
	select {
	case em.matches <- h:
		return true
	case <-em.sc.ctx.Done():
		return false
	}
}

func (em *emitter) finish(partial bool, unreadable int) {
	close(em.matches)
	done := <-em.done
	done.Cancelled = em.sc.ctx.Err() != nil && !done.Truncated
	done.Partial = partial
	done.Unreadable = unreadable
	em.sc.retire()
	em.sc.publish(done)
}

func (em *emitter) collect() {
	var batch []hit
	count := 0
	truncated := false
	var timer *time.Timer
	var due <-chan time.Time

	flush := func() {
		if len(batch) == 0 {
			return
		}
		em.sc.publish(event{Kind: "results", Entries: batch, Count: count})
		batch = nil
	}
	arm := func() {
		if timer == nil {
			timer = time.NewTimer(flushInterval)
			due = timer.C
			return
		}
		timer.Reset(flushInterval)
	}

	for {
		select {
		case h, ok := <-em.matches:
			if !ok {
				flush()
				if timer != nil {
					timer.Stop()
				}
				em.done <- event{Kind: "done", Count: count, Truncated: truncated}
				return
			}
			if truncated {
				continue
			}
			batch = append(batch, h)
			count++
			switch {
			case count >= em.limit:
				truncated = true
				em.sc.cancel()
				flush()
			case len(batch) == 1:
				arm()
			case len(batch) >= batchSize:
				flush()
			}
		case <-due:
			flush()
		}
	}
}

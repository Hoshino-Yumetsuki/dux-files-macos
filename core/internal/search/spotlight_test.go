package search

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spotlightService(t *testing.T, bus Publisher, result spotlightResult, callErr error) *Service {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := newTestService(t, bus)
	svc.SetIndexEnabled(true)
	svc.index.native = func(_ context.Context, method string, params any, out any) error {
		assert.Equal(t, "search.query", method)
		assert.Equal(t, MaxResults, params.(map[string]any)["limit"])
		*out.(*spotlightResult) = result
		return callErr
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSpotlightKeepsPathGlobAndFuzzyMatching(t *testing.T) {
	root := tree(t)
	writeFile(t, filepath.Join(root, "r-e-p-o-r-t.txt"), "x")
	var paths []string
	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)
		return err
	}))
	paths = append(paths, filepath.Join(root, "Report.pdf"), "/foreign/report.txt", filepath.Join(root, "gone-report.txt"))
	for _, tt := range []struct {
		query string
		opts  Options
		want  []string
	}{
		{"docs report", Options{}, []string{"report-2024.txt", "Another Report.md"}},
		{"report *.txt", Options{}, []string{"report-2024.txt"}},
		{"report", Options{Fuzzy: true}, []string{"Report.pdf", "Reports", "report-2024.txt", "Another Report.md", "r-e-p-o-r-t.txt"}},
		{"", Options{Types: []string{"images"}}, []string{"photo.jpg"}},
	} {
		t.Run(tt.query, func(t *testing.T) {
			bus := newBus()
			svc := spotlightService(t, bus, spotlightResult{Paths: paths}, nil)
			_, topic, err := svc.Start(context.Background(), root, tt.query, tt.opts)
			require.NoError(t, err)
			done := bus.waitDone(t)
			assert.ElementsMatch(t, tt.want, bus.names(topic))
			assert.Equal(t, "index", bus.backend(topic).Backend)
			assert.Equal(t, "spotlight", bus.backend(topic).Phase)
			assert.False(t, done.Partial)
		})
	}
}

func TestSpotlightHonoursIgnoreRulesAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\nignored/\n")
	writeFile(t, filepath.Join(root, "src", ".ignore"), "secret*\n!secret-keep.txt\n")
	var paths []string
	for _, rel := range []string{"match.log", "ignored/match.txt", "node_modules/match.txt", "Steam/match.txt", "src/secret-match.txt", "src/secret-keep.txt", "src/match.txt", ".hidden/match.txt"} {
		path := filepath.Join(root, rel)
		writeFile(t, path, "x")
		paths = append(paths, path)
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "linked")))
	paths = append(paths, filepath.Join(root, "node_modules"), filepath.Join(root, "linked", "match.txt"))
	bus := newBus()
	svc := spotlightService(t, bus, spotlightResult{Paths: paths}, nil)
	svc.SetExcludes(func() []string { return []string{"Steam"} })
	_, topic, err := svc.Start(context.Background(), root, "txt", Options{})
	require.NoError(t, err)
	bus.waitDone(t)
	assert.ElementsMatch(t, []string{"match.txt", "secret-keep.txt"}, bus.names(topic))

	_, topic, err = svc.Start(context.Background(), root, "txt", Options{IncludeIgnored: true})
	require.NoError(t, err)
	bus.waitDone(t)
	assert.Len(t, bus.names(topic), 6)
}

func TestSpotlightFallsBackForIncompleteOrUnavailableNames(t *testing.T) {
	root := tree(t)
	for _, tt := range []struct {
		name             string
		result           spotlightResult
		err              error
		opts             Options
		contentAvailable bool
	}{
		{"incomplete", spotlightResult{Paths: []string{filepath.Join(root, "Report.pdf")}, Partial: true}, nil, Options{}, true},
		{"empty index", spotlightResult{}, nil, Options{}, true},
		{"unavailable", spotlightResult{}, errors.New("unavailable"), Options{}, false},
		{"hidden", spotlightResult{}, nil, Options{IncludeHidden: true}, true},
		{"direct", spotlightResult{}, nil, Options{Direct: true}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bus := newBus()
			svc := spotlightService(t, bus, tt.result, tt.err)
			_, topic, err := svc.Start(context.Background(), root, "report", tt.opts)
			require.NoError(t, err)
			done := bus.waitDone(t)
			want := 4
			if tt.opts.IncludeHidden {
				want = 6
			}
			assert.Len(t, bus.names(topic), want)
			assert.Equal(t, "walk", bus.backend(topic).Backend)
			assert.Equal(t, tt.contentAvailable, bus.backend(topic).ContentAvailable)
			assert.False(t, done.Partial)
		})
	}
}

func TestSpotlightContentDoesNotMatchNamesAndHonoursFilters(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, "secret phrase")
	writeFile(t, filepath.Join(root, "secret.txt"), "unrelated")
	writeFile(t, filepath.Join(root, "old.txt"), "secret phrase")
	require.NoError(t, os.Chtimes(filepath.Join(root, "old.txt"), time.Unix(1, 0), time.Unix(1, 0)))
	bus := newBus()
	svc := spotlightService(t, bus, spotlightResult{Paths: []string{path, filepath.Join(root, "old.txt"), root}, Partial: true}, nil)
	_, topic, err := svc.Start(context.Background(), root, "secret", Options{Contents: true, MinSize: 2, MaxSize: 20, ModifiedAfterMs: time.Now().Add(-time.Hour).UnixMilli(), Types: []string{"documents"}})
	require.NoError(t, err)
	assert.True(t, bus.waitDone(t).Partial)
	assert.Equal(t, []string{"notes.txt"}, bus.names(topic))
}

func TestUnavailableContentSearchNeverWalksNames(t *testing.T) {
	root := tree(t)
	for _, native := range []bool{true, false} {
		bus := newBus()
		svc := spotlightService(t, bus, spotlightResult{}, &nativebridge.Error{Code: "NOTSUPPORTED", Message: "Spotlight unavailable"})
		if !native {
			svc.index.native = nil
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		}
		_, topic, err := svc.Start(context.Background(), root, "report", Options{Contents: true})
		require.NoError(t, err)
		done := bus.waitDone(t)
		assert.Zero(t, done.Count)
		assert.True(t, done.Partial)
		assert.Empty(t, bus.names(topic))
		assert.Equal(t, []string{"error", "done"}, bus.kinds(topic))
		bus.mu.Lock()
		failure := bus.events[topic][0]
		bus.mu.Unlock()
		assert.Equal(t, string(files.CodeNotSupported), failure.Code)
		assert.NotEmpty(t, failure.Error)
	}
}

func TestSpotlightContentErrorsPreserveDetails(t *testing.T) {
	bus := newBus()
	failure := &nativebridge.Error{Code: "EACCES", Message: "Spotlight access denied"}
	svc := spotlightService(t, bus, spotlightResult{}, failure)
	_, topic, err := svc.Start(context.Background(), tree(t), "report", Options{Contents: true})
	require.NoError(t, err)
	assert.True(t, bus.waitDone(t).Partial)
	assert.Empty(t, bus.names(topic))
	bus.mu.Lock()
	ev := bus.events[topic][0]
	bus.mu.Unlock()
	assert.Equal(t, "error", ev.Kind)
	assert.Equal(t, failure.Code, ev.Code)
	assert.Equal(t, failure.Error(), ev.Error)
}

func TestSpotlightSendsEverywhereRootsAndContentPredicate(t *testing.T) {
	home, mount := t.TempDir(), t.TempDir()
	bus := newBus()
	svc := spotlightService(t, bus, spotlightResult{}, nil)
	t.Setenv("HOME", home)
	svc.SetMounts(func() []string { return []string{mount, filepath.Join(home, "nested")} })
	called := make(chan map[string]any, 1)
	svc.index.native = func(_ context.Context, _ string, params any, out any) error {
		called <- params.(map[string]any)
		*out.(*spotlightResult) = spotlightResult{}
		return nil
	}
	_, _, err := svc.Start(context.Background(), "/unused", "secret *.txt", Options{Contents: true, Scope: ScopeEverywhere})
	require.NoError(t, err)
	bus.waitDone(t)
	params := <-called
	assert.ElementsMatch(t, []string{home, mount}, params["roots"])
	assert.Equal(t, `kMDItemTextContent == "*secret*"c && kMDItemTextContent == "*.txt"c`, params["predicate"])
}

func TestContentRequiresEnabledIndexQueryAndIndexedMode(t *testing.T) {
	svc := newTestService(t, newBus())
	root := t.TempDir()
	for _, opts := range []Options{{Contents: true}, {Contents: true, Direct: true}} {
		_, _, err := svc.Start(context.Background(), root, "report", opts)
		require.Error(t, err)
	}
	svc.SetIndexEnabled(true)
	_, _, err := svc.Start(context.Background(), root, " ", Options{Contents: true, Types: []string{"documents"}})
	require.Error(t, err)
}

func TestSpotlightCancellationStopsNativeCall(t *testing.T) {
	bus := newBus()
	svc := spotlightService(t, bus, spotlightResult{}, nil)
	started := make(chan struct{})
	svc.index.native = func(ctx context.Context, _ string, _ any, _ any) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, topic, err := svc.Start(ctx, t.TempDir(), "report", Options{Contents: true})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("native query did not start")
	}
	cancel()
	assert.True(t, bus.waitDone(t).Cancelled)
	assert.Equal(t, []string{"done"}, bus.kinds(topic))
}

func TestSpotlightPredicateEscapesLiterals(t *testing.T) {
	assert.Equal(t, `kMDItemFSName == "*"`, spotlightPredicate(parseTerms("report"), Options{}))
	assert.Equal(t, `kMDItemTextContent == "*secret*"c && kMDItemTextContent == "*.txt"c`, spotlightPredicate(parseTerms("secret *.txt"), Options{Contents: true}))
	assert.Equal(t, `kMDItemTextContent == "*a\"b\\c*"c`, spotlightPredicate(parseTerms(`a"b\c`), Options{Contents: true}))
	assert.Equal(t, `kMDItemTextContent == "*s*é*c*r*e*t*"c && kMDItemTextContent == "*.txt"c`, spotlightPredicate(parseTerms("sécret *.txt"), Options{Contents: true, Fuzzy: true}))
}

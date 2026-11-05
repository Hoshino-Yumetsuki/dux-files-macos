package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/nativebridge"
)

type spotlightResult struct {
	Paths   []string `json:"paths"`
	Partial bool     `json:"partial"`
}

func (b *indexBackend) serveSpotlight(sc *search, em *emitter, targets []string, terms []term, opts Options) string {
	if opts.IncludeHidden && !opts.Contents {
		return reasonHidden
	}
	ctx, cancel := context.WithTimeout(sc.ctx, 12*time.Second)
	defer cancel()
	var result spotlightResult
	err := b.native(ctx, "search.query", map[string]any{
		"roots": targets, "predicate": spotlightPredicate(terms, opts), "limit": MaxResults,
	}, &result)
	if sc.ctx.Err() != nil {
		em.finish(false, 0)
		return ""
	}
	if err != nil {
		reason, code := reasonError, string(files.CodeOf(err))
		var nativeErr *nativebridge.Error
		if errors.As(err, &nativeErr) {
			code = nativeErr.Code
			if code == "NOTSUPPORTED" {
				reason = reasonNoIndex
			}
		}
		if opts.Contents {
			sc.publish(event{Kind: "error", Code: code, Reason: reason, Error: err.Error()})
			em.finish(true, 0)
			return ""
		}
		return reason
	}
	if !opts.Contents {
		switch {
		case result.Partial:
			return reasonPartial
		case len(result.Paths) == 0:
			return reasonNotIndexed
		}
	}
	var excludes []string
	if sc.svc.excludes != nil {
		excludes = sc.svc.excludes()
	}
	rules := newIgnoreRules(excludes, opts.IncludeIgnored)
	patterns := make(map[string][]ignorePattern)
	rootDevices := make(map[string]uint64)
	for _, root := range targets {
		patterns[root] = rules.load(root, rules.rootPatterns(root))
		if !opts.CrossDevice {
			if dev, ok := deviceOf(root); ok {
				rootDevices[root] = dev
			}
		}
	}
	indexed := indexResult{Hits: make([]indexHit, 0, len(result.Paths))}
	for _, path := range result.Paths {
		if filepath.IsAbs(path) {
			indexed.Hits = append(indexed.Hits, indexHit{ID: path})
		}
	}
	sc.publish(event{Kind: "backend", Backend: "index", Phase: "spotlight"})
	for _, c := range rankHits(indexed, targets, terms, opts, ignoreRules{}) {
		if sc.ctx.Err() != nil {
			break
		}
		entry, err := sc.svc.stater.Stat(c.path)
		if err != nil || !opts.accepts(entry) || (opts.Contents && entry.IsDir) {
			continue
		}
		root, _ := rootOf(c.path, targets)
		if root == "" {
			root = "/"
		}
		if !spotlightAccepts(c.path, root, entry, opts, rules, patterns, rootDevices) {
			continue
		}
		if !em.emit(hit{Entry: entry, Score: c.score}) {
			break
		}
	}
	em.finish(result.Partial || opts.IncludeHidden, 0)
	return ""
}

func spotlightPredicate(terms []term, opts Options) string {
	// ponytail: broad candidates preserve path/fuzzy semantics; large scopes walk until selective indexing is needed.
	if !opts.Contents {
		return `kMDItemFSName == "*"`
	}
	clauses := make([]string, 0, len(terms))
	for _, t := range terms {
		pattern := t.text
		switch {
		case t.glob:
		case opts.Fuzzy:
			pattern = "*" + strings.Join(strings.Split(pattern, ""), "*") + "*"
		default:
			pattern = "*" + pattern + "*"
		}
		pattern = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(pattern)
		clauses = append(clauses, `kMDItemTextContent == "`+pattern+`"c`)
	}
	return strings.Join(clauses, " && ")
}

func spotlightAccepts(path, root string, entry files.Entry, opts Options, rules ignoreRules, cache map[string][]ignorePattern, devices map[string]uint64) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, name := range parts {
		if !opts.IncludeHidden && strings.HasPrefix(name, ".") {
			return false
		}
		full := filepath.Join(dir, name)
		parent := i < len(parts)-1
		isDir := parent || (entry.IsDir && !entry.IsSymlink)
		if (isDir && rules.skipsDir(name, full)) || ignored(cache[dir], full, isDir) {
			return false
		}
		if !parent {
			return true
		}
		if _, found := cache[full]; !found {
			info, err := os.Lstat(full)
			if err != nil || !info.IsDir() {
				return false
			}
			if dev, ok := devices[root]; ok {
				if current, ok := deviceOf(full); ok && current != dev {
					return false
				}
			}
			cache[full] = rules.load(full, cache[dir])
		}
		dir = full
	}
	return false
}

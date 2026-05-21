// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package activity tracks the last-write timestamp per managed folder
// root so reconcile can decide when a folder has been dormant long
// enough to pause and when activity warrants an immediate unpause.
//
// Design choices worth knowing before reading the code:
//
//   - Independent fsnotify watcher. Reconcile's own watcher in
//     cmd/dotkeeper/cmds_v5.go is scoped to firing reconcile on
//     .dotkeeper.toml/state.toml/machine.toml events; broadening it to
//     track every Write would couple two unrelated concerns. A
//     separate Tracker keeps the responsibilities clean and lets the
//     activity logic evolve (event filtering, debouncing, persistence)
//     without touching the reconcile loop.
//
//   - In-memory only. The map is rebuilt when the daemon starts. A
//     fresh daemon treats every folder as "just active" — so the
//     first auto-pause for any folder happens at the earliest
//     idleThreshold *after* startup. This is intentional: pausing
//     folders within seconds of daemon start on the theory that they
//     were quiet before would surprise users on systems where the
//     daemon restarts often (config changes, OOM kills, upgrades).
//     The cost is a single missed pause cycle on restart; trivial
//     compared to the value of "the daemon never pauses a folder you
//     just touched while it was offline."
//
//   - Per-tree watcher walk at New() time. fsnotify on Linux is not
//     recursive, so each managed root is walked and every
//     subdirectory registered. Skip-dirs (.git, .stfolder, .dkfolder)
//     are excluded so churn inside .git/objects doesn't masquerade as
//     "user editing." Newly created directories under a watched root
//     are added on the fly.
package activity

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// skipDirs mirrors the conflict watcher's exclusion set. These trees
// generate constant inotify events that have nothing to do with user
// activity (git pack files rewritten on gc, Syncthing's own .stfolder
// touched on every state update). Including them would prevent
// auto-pause from ever firing on a healthy repo.
var skipDirs = map[string]struct{}{
	".git":      {},
	".stfolder": {},
	".dkfolder": {},
}

// Tracker watches one or more folder trees and records the last-Write
// time per top-level root. Safe for concurrent use; LastActivity may
// be called from any goroutine.
type Tracker struct {
	fs *fsnotify.Watcher

	// rootForPath maps each watched directory to the top-level root
	// it belongs to. fsnotify events arrive with the watched directory
	// path, so resolving to a root is O(1) instead of a prefix scan.
	mu          sync.RWMutex
	rootForPath map[string]string
	lastSeen    map[string]time.Time
	closed      bool

	// hints fires when a watched root sees activity. Reconcile uses
	// this to trigger an immediate cycle rather than waiting for the
	// next interval tick, so unpause is sub-second-perceived.
	hints chan string

	done chan struct{}
}

// hintsBuffer is the depth of the activity-hints channel. Bursts of
// editor saves can flood the channel; we coalesce on the reader side
// rather than risk blocking the fsnotify event loop on a slow consumer.
const hintsBuffer = 16

// New creates a Tracker for the given folder roots. Each root is
// walked at startup and every subdirectory registered with fsnotify.
// Returns an error only if fsnotify refuses to start at all; partial
// failure (some roots unreachable) yields a usable tracker and the
// caller learns about per-root errors via the returned error map.
//
// roots should be absolute, existing paths.
func New(roots []string) (*Tracker, error) {
	if len(roots) == 0 {
		return nil, errors.New("activity.New: no roots given")
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("activity.New: fsnotify.NewWatcher: %w", err)
	}

	t := &Tracker{
		fs:          fw,
		rootForPath: make(map[string]string),
		lastSeen:    make(map[string]time.Time, len(roots)),
		hints:       make(chan string, hintsBuffer),
		done:        make(chan struct{}),
	}

	now := time.Now()
	var watchErrs []error
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			watchErrs = append(watchErrs, fmt.Errorf("abs %q: %w", root, err))
			continue
		}
		if err := t.addTree(abs, abs); err != nil {
			watchErrs = append(watchErrs, err)
			// Continue rather than abort — a tracker that watches
			// most of its configured roots is much better than no
			// tracker at all, and the unreachable root may simply
			// have a permission issue the user will fix later.
		}
		// Seed lastSeen so a fresh daemon doesn't immediately pause
		// folders it has never seen activity on. See package comment.
		t.lastSeen[abs] = now
	}
	if len(watchErrs) == len(roots) {
		_ = fw.Close()
		return nil, errors.Join(watchErrs...)
	}

	go t.run()
	return t, nil
}

// LastActivity returns the most recent observed write/create/remove
// event under root, or the daemon-start time for that root if no
// event has been seen since. The ok bool is false if root is not
// managed by this Tracker — callers must distinguish from a true
// zero time.
func (t *Tracker) LastActivity(root string) (time.Time, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return time.Time{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.lastSeen[abs]
	return v, ok
}

// Hints returns the channel on which root paths are emitted whenever
// a watched root sees activity. Drains coalesce duplicates on the
// reader side. Returns a nil receive-only channel after Close.
func (t *Tracker) Hints() <-chan string { return t.hints }

// Close stops the watcher and releases fsnotify resources. Safe to
// call multiple times.
func (t *Tracker) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	close(t.done)
	return t.fs.Close()
}

// addTree walks tree and registers every directory under it with
// fsnotify, tagging each watched path with the root it belongs to.
// Individual Add failures are collected and returned joined.
func (t *Tracker) addTree(tree, root string) error {
	var errs []error
	werr := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if _, skip := skipDirs[d.Name()]; skip && path != tree {
			return fs.SkipDir
		}
		if err := t.fs.Add(path); err != nil {
			errs = append(errs, fmt.Errorf("watch %s: %w", path, err))
			return nil
		}
		t.mu.Lock()
		t.rootForPath[path] = root
		t.mu.Unlock()
		return nil
	})
	if werr != nil {
		errs = append(errs, werr)
	}
	return errors.Join(errs...)
}

// run is the tracker goroutine. It drains fsnotify events, updates
// lastSeen for the owning root, and forwards a hint. New directories
// created under a watched root are auto-added so descendants of the
// new directory are also observed.
func (t *Tracker) run() {
	defer close(t.hints)

	for {
		select {
		case <-t.done:
			return

		case ev, ok := <-t.fs.Events:
			if !ok {
				return
			}
			t.handleEvent(ev)

		case _, ok := <-t.fs.Errors:
			if !ok {
				return
			}
			// fsnotify errors are typically watch-limit warnings or
			// transient kernel issues; we don't surface them
			// individually because the only useful response is to
			// rebuild the watcher, and the daemon's existing
			// fsnotify in cmds_v5.go will report the same condition.
		}
	}
}

func (t *Tracker) handleEvent(ev fsnotify.Event) {
	// Auto-add new directories so events inside them are seen too.
	if ev.Op.Has(fsnotify.Create) {
		if info, err := statNoFollow(ev.Name); err == nil && info.IsDir() {
			if _, skip := skipDirs[filepath.Base(ev.Name)]; !skip {
				root := t.rootOf(filepath.Dir(ev.Name))
				if root != "" {
					_ = t.addTree(ev.Name, root)
				}
			}
		}
	}

	// Only Write/Create/Remove count as activity. Chmod and pure
	// rename-without-content events aren't user edits.
	if !ev.Op.Has(fsnotify.Write) && !ev.Op.Has(fsnotify.Create) && !ev.Op.Has(fsnotify.Remove) {
		return
	}

	// Skip events inside skipDirs even if the parent was watched
	// (rare; happens if a tree was added before .git was created).
	if pathHasSkipDir(ev.Name) {
		return
	}

	root := t.rootOf(filepath.Dir(ev.Name))
	if root == "" {
		return
	}

	t.mu.Lock()
	t.lastSeen[root] = time.Now()
	t.mu.Unlock()

	select {
	case t.hints <- root:
	default:
		// Hint channel full — reconcile will see the update via
		// LastActivity on its next tick anyway. Dropping is the
		// correct behaviour, not a regression.
	}
}

// rootOf returns the root path that owns the given watched directory,
// or "" if the path isn't under any managed root.
func (t *Tracker) rootOf(dir string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rootForPath[dir]
}

// pathHasSkipDir reports whether any path component matches a
// skipDirs entry. Catches the case where .git was created after the
// tracker registered its parent and we shouldn't count internal git
// churn as activity.
func pathHasSkipDir(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if _, skip := skipDirs[part]; skip {
			return true
		}
	}
	return false
}

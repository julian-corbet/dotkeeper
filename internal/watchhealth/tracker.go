// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package watchhealth

import (
	"sync"
	"time"
)

// Tracker accumulates per-folder health signals. Three sources feed
// into it:
//
//  1. Filesystem classification at folder registration time (via
//     detectFilesystem, called from Register).
//  2. Overflow flags raised by the activity tracker when its
//     fsnotify watcher reports IN_Q_OVERFLOW (Linux) or
//     ERROR_NOTIFY_ENUM_DIR (Windows).
//  3. Watch-limit-hit flags raised when fsnotify's Add() returns
//     ENOSPC.
//
// The Status reads via Status(path) consume the cumulative state
// since the last Reset(path). Reconcile is expected to Reset() each
// folder after acting on its status, so the next cycle starts from
// a clean flag set.
type Tracker struct {
	mu       sync.Mutex
	statuses map[string]*Status
}

// New creates an empty Tracker. Folders are registered via Register
// as they appear in the managed set.
func New() *Tracker {
	return &Tracker{statuses: make(map[string]*Status)}
}

// Register records a folder's filesystem classification. Idempotent
// — re-registering an already-known path updates the FS info but
// preserves OverflowSeen / WatchLimitHit flags from earlier
// observations.
func (t *Tracker) Register(path string) Status {
	kind, fsName := detectFilesystem(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		s = &Status{Path: path}
		t.statuses[path] = s
	}
	s.FilesystemType = fsName
	s.Kind = kind
	return *s
}

// Unregister drops a path from the tracker. Used when reconcile
// removes a folder so the tracker doesn't accumulate stale entries.
func (t *Tracker) Unregister(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.statuses, path)
}

// MarkOverflow records that the OS event API signalled it dropped
// events for this folder. Idempotent within a reconcile cycle —
// multiple overflow signals between Reset() calls collapse into a
// single OverflowSeen=true.
func (t *Tracker) MarkOverflow(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		// Unknown folder; nothing to mark. Register hasn't been
		// called yet, which is a race during startup we tolerate by
		// dropping the signal — the next reconcile cycle will
		// reclassify the folder and the user-visible behaviour
		// (next-tick rescan) is identical to the explicit-overflow
		// path.
		return
	}
	s.OverflowSeen = true
}

// MarkWatchLimitHit records an ENOSPC (or equivalent) when adding
// watches for this folder. Reconcile treats this as "untrusted
// watcher" until cleared, which means periodic rescans stay on.
func (t *Tracker) MarkWatchLimitHit(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		return
	}
	s.WatchLimitHit = true
}

// MarkEventDelivered updates LastReliableEventAt so reconcile can
// reason about "watcher has been silent for too long; rescan as a
// liveness check." Called by the activity tracker on every
// Write/Create/Remove that maps to a managed folder.
func (t *Tracker) MarkEventDelivered(path string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		return
	}
	s.LastReliableEventAt = at
}

// Status implements the Querier interface. Returns a snapshot copy
// of the per-folder status so the caller can read fields without
// holding the tracker's lock.
func (t *Tracker) Status(path string) (Status, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		return Status{}, false
	}
	return *s, true
}

// Reset clears the one-shot flags (OverflowSeen, WatchLimitHit) for
// path so the next reconcile cycle starts from a clean slate.
// Filesystem classification and LastReliableEventAt persist —
// they're true facts about the world, not pending signals to react
// to.
//
// Reset is normally called by the applier after a RescanFolderNow
// action runs successfully. If reconcile observes OverflowSeen
// without taking action (rare), the flag stays set and the next
// cycle gets another chance to act.
func (t *Tracker) Reset(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.statuses[path]
	if !ok {
		return
	}
	s.OverflowSeen = false
	s.WatchLimitHit = false
}

// AllPaths returns the set of currently-registered folder paths.
// Used by reconcile to iterate (e.g. when fanning out wake-driven
// rescans to every folder).
func (t *Tracker) AllPaths() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	paths := make([]string, 0, len(t.statuses))
	for p := range t.statuses {
		paths = append(paths, p)
	}
	return paths
}

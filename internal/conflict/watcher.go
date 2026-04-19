// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watcher observes one or more folder trees and emits a Conflict on its
// Events() channel whenever a sync-conflict file appears.
//
// fsnotify is non-recursive on Linux and most platforms, so the watcher
// walks each root at startup and hooks Watch on every directory it
// finds. When a new directory is created later, we detect the Create
// event and recursively add that tree too.
//
// The event channel is buffered; if the consumer falls behind, the
// watcher logs and drops events rather than blocking the fsnotify
// goroutine (which could stall the kernel-side queue).
type Watcher struct {
	fs *fsnotify.Watcher

	events  chan Conflict
	errs    chan error
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

// eventBufferSize is the depth of the events channel. 64 is generous
// enough that typical bursts (a whole folder of conflicts appearing at
// once after a Syncthing reconciliation) don't get dropped.
const eventBufferSize = 64

// New creates a Watcher for the given root directories. Each root is
// walked and every sub-directory registered with fsnotify. Returns an
// error if fsnotify refuses to start or if no roots are watchable.
func New(roots []string) (*Watcher, error) {
	if len(roots) == 0 {
		return nil, errors.New("conflict.New: no roots given")
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify.NewWatcher: %w", err)
	}

	w := &Watcher{
		fs:     fw,
		events: make(chan Conflict, eventBufferSize),
		errs:   make(chan error, 16),
		done:   make(chan struct{}),
	}

	var watchErrs []error
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			watchErrs = append(watchErrs, fmt.Errorf("abs %q: %w", root, err))
			continue
		}
		if err := w.addTree(abs); err != nil {
			watchErrs = append(watchErrs, err)
		}
	}
	// If none of the roots could be registered, don't start the loop.
	// A partial success is OK — the caller logs watchErrs via Errors().
	if len(watchErrs) == len(roots) {
		_ = fw.Close()
		return nil, errors.Join(watchErrs...)
	}

	go w.run()
	return w, nil
}

// Events returns the channel on which conflicts appear. It is closed
// when Close() is called.
func (w *Watcher) Events() <-chan Conflict { return w.events }

// Errors returns the channel on which non-fatal fsnotify errors are
// reported. It is closed when Close() is called.
func (w *Watcher) Errors() <-chan error { return w.errs }

// Close stops the watcher and releases fsnotify resources. Safe to call
// multiple times.
func (w *Watcher) Close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	close(w.done)
	return w.fs.Close()
}

// addTree registers root and every sub-directory with fsnotify.
// Skip-list directories are skipped for the same reasons as Scan.
// Individual Add failures are collected and returned joined — we don't
// give up on the whole tree if one directory is unreadable.
func (w *Watcher) addTree(root string) error {
	var errs []error
	werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if _, skip := skipDirs[d.Name()]; skip && path != root {
			return fs.SkipDir
		}
		if err := w.fs.Add(path); err != nil {
			errs = append(errs, fmt.Errorf("watch %s: %w", path, err))
		}
		return nil
	})
	if werr != nil {
		errs = append(errs, werr)
	}
	return errors.Join(errs...)
}

// run is the watcher goroutine. It forwards matching fsnotify events as
// Conflicts, auto-adds newly created directories, and shuts down
// cleanly when Close() is invoked.
func (w *Watcher) run() {
	defer close(w.events)
	defer close(w.errs)

	for {
		select {
		case <-w.done:
			return

		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)

		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			// Best-effort error surfacing. Drop rather than block.
			select {
			case w.errs <- err:
			default:
			}
		}
	}
}

// handleEvent dispatches a single fsnotify event. Create+Write on
// conflict files become Conflict emissions; Create on directories
// extends the watch set.
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	// Watch new directories so files created inside them are also seen.
	// Rename/Move can land a directory here too (as a Create), so this
	// covers both "mkdir" and "mv dir" cases.
	if ev.Op.Has(fsnotify.Create) {
		if info, err := statNoFollow(ev.Name); err == nil && info.IsDir() {
			if _, skip := skipDirs[filepath.Base(ev.Name)]; !skip {
				if err := w.addTree(ev.Name); err != nil {
					select {
					case w.errs <- err:
					default:
					}
				}
			}
		}
	}

	// We only care about appearances. Syncthing writes the conflict file
	// atomically, so Create is the canonical trigger; Write is added
	// defensively in case a platform splits the event.
	if !ev.Op.Has(fsnotify.Create) && !ev.Op.Has(fsnotify.Write) {
		return
	}

	c, err := Parse(filepath.Base(ev.Name))
	if err != nil {
		return
	}
	abs, err := filepath.Abs(ev.Name)
	if err != nil {
		abs = ev.Name
	}
	c.Path = abs

	select {
	case w.events <- *c:
	case <-w.done:
	default:
		// Consumer too slow. Drop and signal; we'd rather lose a line
		// than hold up the kernel-side queue.
		select {
		case w.errs <- fmt.Errorf("conflict event dropped: %s", abs):
		default:
		}
	}
}

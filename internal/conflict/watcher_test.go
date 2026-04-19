// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFor blocks until fn returns a non-nil Conflict, timing out after d.
// Using a helper rather than a channel drain keeps tests readable and
// lets us accept events from any point in the stream, which is the
// useful invariant (events DID arrive), not the scheduling one.
func waitFor(t *testing.T, ch <-chan Conflict, d time.Duration, match func(Conflict) bool) *Conflict {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("events channel closed before expected event")
			}
			if match(c) {
				return &c
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for expected conflict event", d)
		}
	}
}

// TestWatcherEmitsConflictOnCreate drops a conflict file into a watched
// directory and asserts the watcher surfaces it on the events channel.
func TestWatcherEmitsConflictOnCreate(t *testing.T) {
	root := t.TempDir()

	w, err := New([]string{root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	target := filepath.Join(root, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := waitFor(t, w.Events(), 3*time.Second, func(c Conflict) bool {
		return c.Path == target
	})
	if got.OriginalName != "notes.md" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, "notes.md")
	}
	if got.DeviceIDShort != "UUS6FSQ" {
		t.Errorf("DeviceIDShort = %q, want UUS6FSQ", got.DeviceIDShort)
	}
}

// TestWatcherIgnoresNonConflictFiles writes plain files and checks
// nothing arrives on the events channel within a short grace period.
func TestWatcherIgnoresNonConflictFiles(t *testing.T) {
	root := t.TempDir()

	w, err := New([]string{root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := os.WriteFile(filepath.Join(root, "normal.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-w.Events():
		t.Fatalf("unexpected conflict event: %+v", c)
	case <-time.After(300 * time.Millisecond):
		// OK — nothing arrived, as expected.
	}
}

// TestWatcherAutoWatchesNewDirs creates a subdirectory after the watcher
// is running, then drops a conflict file into it. The watcher must
// notice the new dir and emit the conflict.
func TestWatcherAutoWatchesNewDirs(t *testing.T) {
	root := t.TempDir()

	w, err := New([]string{root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	sub := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Give fsnotify a brief moment to process the directory-create
	// events and register the new paths. 100ms is generous for a local
	// tempdir on all CI platforms we target.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(sub, "config.sync-conflict-20260419-143015-WB25TET.toml")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, w.Events(), 3*time.Second, func(c Conflict) bool {
		return c.Path == target
	})
}

// TestWatcherSkipsSkipDirs confirms that conflicts appearing inside a
// skip-list directory (.git, .stfolder, .dkfolder) are not surfaced.
func TestWatcherSkipsSkipDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := New([]string{root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Drop a conflict file directly into the pre-existing .git dir.
	ignored := filepath.Join(root, ".git", "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err := os.WriteFile(ignored, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// And a real conflict in the root, so we know the watcher is live.
	real := filepath.Join(root, "real.sync-conflict-20260419-143015-WB25TET.toml")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := waitFor(t, w.Events(), 3*time.Second, func(c Conflict) bool {
		return c.Path == real
	})
	if got == nil {
		t.Fatal("expected real conflict but waitFor returned nil")
	}

	// Drain briefly and make sure the ignored one never appeared.
	drain := time.After(200 * time.Millisecond)
	for {
		select {
		case c := <-w.Events():
			if c.Path == ignored {
				t.Errorf("conflict inside .git should have been ignored: %s", c.Path)
			}
		case <-drain:
			return
		}
	}
}

// TestWatcherCloseIdempotent verifies Close can be called more than
// once without panicking or returning an error on the second call.
func TestWatcherCloseIdempotent(t *testing.T) {
	w, err := New([]string{t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestWatcherNoRoots rejects empty-input calls up front.
func TestWatcherNoRoots(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("expected error for nil roots")
	}
	if _, err := New([]string{}); err == nil {
		t.Error("expected error for empty roots")
	}
}

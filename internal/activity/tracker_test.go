// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSeedsLastSeenAtStartup(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().Add(-time.Second) // generous margin for slow CI

	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	got, ok := tr.LastActivity(dir)
	if !ok {
		t.Fatalf("LastActivity(%q) returned ok=false; expected the seeded startup time", dir)
	}
	if got.Before(before) {
		t.Errorf("seeded time %v is before test-start %v; should be ~now at New()", got, before)
	}
}

func TestLastActivityUpdatesOnWrite(t *testing.T) {
	dir := t.TempDir()
	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	seed, _ := tr.LastActivity(dir)

	// A spin loop on the lastSeen value is unreliable on slow CI; we
	// wait for the hint instead. The fsnotify-to-handler-to-LastActivity
	// path runs entirely in-process so the wait is bounded by goroutine
	// scheduling, not real I/O.
	time.Sleep(20 * time.Millisecond) // ensure the new mtime is after seed

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case got := <-tr.Hints():
		if got != dir {
			t.Errorf("hint root = %q, want %q", got, dir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no activity hint within 2s; tracker did not observe the write")
	}

	updated, ok := tr.LastActivity(dir)
	if !ok {
		t.Fatalf("LastActivity dropped the root")
	}
	if !updated.After(seed) {
		t.Errorf("LastActivity not advanced: seed=%v updated=%v", seed, updated)
	}
}

func TestSkipDirsIgnored(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("Mkdir .git: %v", err)
	}

	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	seed, _ := tr.LastActivity(dir)

	// Write inside .git. Should NOT emit a hint nor advance LastActivity.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: x"), 0o644); err != nil {
		t.Fatalf("WriteFile inside .git: %v", err)
	}

	select {
	case got := <-tr.Hints():
		t.Errorf("activity hint fired for path inside .git: %q", got)
	case <-time.After(300 * time.Millisecond):
		// Expected: no hint.
	}

	updated, _ := tr.LastActivity(dir)
	if updated.After(seed) {
		t.Errorf("LastActivity advanced on .git churn (seed=%v updated=%v); .git should be excluded", seed, updated)
	}
}

func TestNewDirectoryAutoWatched(t *testing.T) {
	dir := t.TempDir()
	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	// Create a fresh subdirectory after New(). The auto-add path
	// must register it so subsequent writes inside it are observed.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir sub: %v", err)
	}
	// Drain the create hint for the directory itself before testing
	// the descendant.
drainCreate:
	for {
		select {
		case <-tr.Hints():
		case <-time.After(200 * time.Millisecond):
			break drainCreate
		}
	}

	if err := os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile in sub: %v", err)
	}

	select {
	case got := <-tr.Hints():
		if got != dir {
			t.Errorf("hint root = %q, want %q (new sub-directory was not auto-watched)", got, dir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hint after writing to a sub-directory created post-New(); auto-watch broke")
	}
}

func TestLastActivityUnknownRoot(t *testing.T) {
	dir := t.TempDir()
	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	_, ok := tr.LastActivity("/some/path/never/registered")
	if ok {
		t.Error("LastActivity returned ok=true for an unknown root; callers can't distinguish from zero-time")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tr, err := New([]string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestNewRejectsEmptyRoots(t *testing.T) {
	_, err := New(nil)
	if err == nil {
		t.Error("New(nil) should error; got nil")
	}
}

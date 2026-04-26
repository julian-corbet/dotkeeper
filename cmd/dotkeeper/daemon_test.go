// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/reconcile"
)

// stubReconciler is an in-process stand-in for reconcile.Reconciler that records
// every call to Reconcile so tests can assert on trigger behaviour.
//
// Used by the daemon-loop unit tests below — they exercise the fsnotify +
// timer + serialisation logic in cmds_v5.go without spinning up the full
// dotkeeper binary or Syncthing.
type stubReconciler struct {
	mu          sync.Mutex
	calls       int32
	delay       time.Duration // optional pause inside Reconcile to provoke overlap
	started     chan struct{} // closed on first call (non-blocking signal)
	startedOnce sync.Once
	concurrent  int32 // peak number of in-flight Reconcile calls
	inFlight    int32
}

func newStubReconciler() *stubReconciler {
	return &stubReconciler{started: make(chan struct{})}
}

func (s *stubReconciler) Reconcile(ctx context.Context) (reconcile.Plan, error) {
	atomic.AddInt32(&s.calls, 1)
	s.startedOnce.Do(func() { close(s.started) })

	now := atomic.AddInt32(&s.inFlight, 1)
	for {
		peak := atomic.LoadInt32(&s.concurrent)
		if now <= peak || atomic.CompareAndSwapInt32(&s.concurrent, peak, now) {
			break
		}
	}
	defer atomic.AddInt32(&s.inFlight, -1)

	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
		}
	}
	return reconcile.Plan{}, nil
}

func (s *stubReconciler) Calls() int      { return int(atomic.LoadInt32(&s.calls)) }
func (s *stubReconciler) Concurrent() int { return int(atomic.LoadInt32(&s.concurrent)) }

// silentLogger returns a slog.Logger that discards everything — keeps test
// output readable while exercising paths that log.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestDaemonRunsInitialReconcileBeforeFirstTimerTick verifies that
// startReconcileLoop fires a reconcile immediately on entry, without waiting
// for the first ticker tick. Without this, a daemon with the default 5-min
// interval would do nothing for 5 minutes after boot.
func TestDaemonRunsInitialReconcileBeforeFirstTimerTick(t *testing.T) {
	stub := newStubReconciler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Long interval — if anything fires within 200ms, it MUST be the initial pass.
	startReconcileLoop(ctx, stub, time.Hour, nil, silentLogger())

	select {
	case <-stub.started:
		// Good — initial reconcile fired.
	case <-time.After(2 * time.Second):
		t.Fatalf("daemon did not run initial reconcile within 2s; ticker interval was 1h")
	}

	if got := stub.Calls(); got != 1 {
		t.Errorf("expected exactly 1 reconcile call (the initial); got %d", got)
	}
}

// TestDaemonSerializesConcurrentTriggers verifies that overlapping triggers
// (e.g. timer tick + fsnotify event) never run reconcile concurrently.
// The Reconciler writes state.toml, so concurrent runs would race.
func TestDaemonSerializesConcurrentTriggers(t *testing.T) {
	stub := newStubReconciler()
	stub.delay = 200 * time.Millisecond // hold each reconcile long enough to overlap
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 50ms ticker — fires roughly 4 times during a 200ms reconcile.
	startReconcileLoop(ctx, stub, 50*time.Millisecond, nil, silentLogger())

	// Let the loop run for a bit so multiple triggers stack up.
	time.Sleep(800 * time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond) // give the goroutine time to exit

	if peak := stub.Concurrent(); peak > 1 {
		t.Errorf("expected reconcile calls to be serialised; peak concurrent in-flight = %d", peak)
	}
}

// TestDaemonReconcilesOnNewRepoInScanRootSubdir verifies that creating a new
// repo subdirectory inside a scan root, then dropping a dotkeeper.toml inside
// it, triggers a reconcile. fsnotify is non-recursive by default — the daemon
// must walk the scan root and watch every directory, plus auto-watch new
// subdirectories as they appear.
func TestDaemonReconcilesOnNewRepoInScanRootSubdir(t *testing.T) {
	stub := newStubReconciler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scanRoot := t.TempDir()

	// Long ticker so any reconcile we observe must come from fsnotify.
	startReconcileLoop(ctx, stub, time.Hour, []string{scanRoot}, silentLogger())

	// Wait for the initial reconcile to settle so we count subsequent ones.
	<-stub.started
	initial := stub.Calls()

	// Create a new subdir + dotkeeper.toml well after the watcher is up.
	time.Sleep(200 * time.Millisecond)
	repoDir := filepath.Join(scanRoot, "newrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Give the watcher a moment to pick up the new directory and add it.
	time.Sleep(300 * time.Millisecond)

	tomlPath := filepath.Join(repoDir, "dotkeeper.toml")
	if err := os.WriteFile(tomlPath, []byte("schema_version = 2\n"), 0o644); err != nil {
		t.Fatalf("write dotkeeper.toml: %v", err)
	}

	// Wait for fsnotify (typically <100ms) + 1s debounce + processing.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stub.Calls() > initial {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("daemon did not reconcile after dotkeeper.toml appeared in scan-root subdir; calls=%d, initial=%d", stub.Calls(), initial)
}

// TestDaemonReconcilesOnDotKeeperTomlInWatchedRoot verifies the simpler case:
// dropping a dotkeeper.toml directly in the watched scan root (no subdir)
// triggers reconcile. This is a regression guard for the basename filter.
func TestDaemonReconcilesOnDotKeeperTomlInWatchedRoot(t *testing.T) {
	stub := newStubReconciler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scanRoot := t.TempDir()

	startReconcileLoop(ctx, stub, time.Hour, []string{scanRoot}, silentLogger())

	<-stub.started
	initial := stub.Calls()

	time.Sleep(200 * time.Millisecond)
	tomlPath := filepath.Join(scanRoot, "dotkeeper.toml")
	if err := os.WriteFile(tomlPath, []byte("schema_version = 2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stub.Calls() > initial {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("daemon did not reconcile after dotkeeper.toml appeared directly in watched root; calls=%d", stub.Calls())
}

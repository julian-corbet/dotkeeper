// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package watchhealth

import (
	"context"
	"testing"
	"time"
)

func TestTrackerRegisterClassifiesPath(t *testing.T) {
	tr := New()
	// TempDir lands on whatever the test host's tmp filesystem is —
	// tmpfs on most Linux distros, apfs on macOS, NTFS on Windows,
	// all members of their respective reliable sets. We don't pin
	// the FilesystemKind here because that would make the test
	// hostile to running on exotic build hosts; instead we assert
	// "Register returned a Status with a non-empty FilesystemType
	// OR Kind=Unknown (acknowledging detection failure)." On any
	// production-grade FS one of those is true.
	dir := t.TempDir()
	st := tr.Register(dir)
	if st.Path != dir {
		t.Errorf("Path = %q, want %q", st.Path, dir)
	}
	if st.Kind == FilesystemUnreliable {
		t.Errorf("temp dir classified as Unreliable; FilesystemType=%q — unexpected, check classifier", st.FilesystemType)
	}
}

func TestTrackerRegisterIdempotentPreservesFlags(t *testing.T) {
	tr := New()
	dir := t.TempDir()
	tr.Register(dir)
	tr.MarkOverflow(dir)
	st, ok := tr.Status(dir)
	if !ok || !st.OverflowSeen {
		t.Fatalf("expected OverflowSeen=true after MarkOverflow; got Status=%+v ok=%v", st, ok)
	}
	// Re-register: must not clear the flag.
	tr.Register(dir)
	st, _ = tr.Status(dir)
	if !st.OverflowSeen {
		t.Errorf("re-register cleared OverflowSeen; flags must persist until explicit Reset")
	}
}

func TestTrackerResetClearsPendingFlags(t *testing.T) {
	tr := New()
	dir := t.TempDir()
	tr.Register(dir)
	tr.MarkOverflow(dir)
	tr.MarkWatchLimitHit(dir)
	tr.MarkEventDelivered(dir, time.Now())

	tr.Reset(dir)
	st, ok := tr.Status(dir)
	if !ok {
		t.Fatal("Reset dropped the entry; should clear flags only")
	}
	if st.OverflowSeen {
		t.Error("OverflowSeen not cleared by Reset")
	}
	if st.WatchLimitHit {
		t.Error("WatchLimitHit not cleared by Reset")
	}
	if st.LastReliableEventAt.IsZero() {
		t.Error("LastReliableEventAt was cleared by Reset; that field tracks a fact, not a pending signal")
	}
	if st.FilesystemType == "" {
		t.Error("FilesystemType was cleared by Reset; classification is a fact, not a pending signal")
	}
}

func TestTrackerUnregisterDropsEntry(t *testing.T) {
	tr := New()
	dir := t.TempDir()
	tr.Register(dir)
	tr.Unregister(dir)
	if _, ok := tr.Status(dir); ok {
		t.Error("Status returned ok=true after Unregister")
	}
}

func TestTrackerMarkOnUnknownPathIsNoop(t *testing.T) {
	tr := New()
	// No Register call. These must not panic and must leave the
	// tracker empty.
	tr.MarkOverflow("/nowhere")
	tr.MarkWatchLimitHit("/nowhere")
	tr.MarkEventDelivered("/nowhere", time.Now())
	if len(tr.AllPaths()) != 0 {
		t.Errorf("Mark* on unknown path created an entry; AllPaths=%v", tr.AllPaths())
	}
}

func TestSleepDetectorFiresOnLargeGap(t *testing.T) {
	// Inject a synthetic tick channel so the test controls exactly
	// what the detector observes. Real time.Ticker can't be tested
	// for "wall-clock jumped during sleep" because the test runner
	// doesn't actually sleep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 4)
	events := make(chan SleepEvent, 4)
	go runSleepDetector(ctx, ticks, 100*time.Millisecond, events)

	base := time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC)
	ticks <- base                            // baseline; no event
	ticks <- base.Add(50 * time.Millisecond) // below threshold; no event
	ticks <- base.Add(10 * time.Minute)      // huge gap; expect event

	select {
	case ev := <-events:
		if ev.Gap < 100*time.Millisecond {
			t.Errorf("event reported Gap=%v; expected >100ms", ev.Gap)
		}
		if !ev.DetectedAt.Equal(base.Add(10 * time.Minute)) {
			t.Errorf("DetectedAt = %v, want %v", ev.DetectedAt, base.Add(10*time.Minute))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("no event after a 10-minute synthetic gap")
	}

	// Confirm no event for the sub-threshold gap.
	select {
	case ev := <-events:
		t.Errorf("unexpected second event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
		// pass
	}
}

func TestSleepDetectorRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := StartSleepDetector(ctx, 30*time.Second)
	cancel()
	// Channel must close after context cancellation.
	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected channel close after ctx.Cancel; received a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close within 2s of context cancellation")
	}
}

func TestSleepDetectorClampsZeroInterval(t *testing.T) {
	// interval=0 must not divide by zero or spin. The implementation
	// is allowed to substitute a sensible default; we assert it
	// doesn't panic and produces a usable channel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartSleepDetector(ctx, 0)
	if ch == nil {
		t.Fatal("StartSleepDetector(0) returned nil channel")
	}
}

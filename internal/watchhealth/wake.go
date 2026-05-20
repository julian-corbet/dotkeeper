// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package watchhealth

import (
	"context"
	"time"
)

// SleepDetector fires on the returned channel whenever the wall
// clock advances more than 2× the heartbeat interval between two
// consecutive ticks. This catches:
//
//   - laptop lid close / open (Linux suspend, macOS sleep, Windows
//     S3/S4)
//   - VM pause / resume (host suspends the guest's vCPU; the next
//     tick fires with a large gap)
//   - container clock-jump if the host's clock was set forward
//     while the container was idle (rare, but happens)
//
// The detection is intentionally platform-agnostic. D-Bus signals
// (PrepareForSleep on Linux) and platform-specific APIs (NSWorkspace
// notifications on macOS, WM_POWERBROADCAST on Windows) would each
// catch a subset of these events more precisely, at the cost of
// per-platform code, build tags, and a dependency on systemd / Cocoa
// / Win32 APIs. The wall-clock-gap approximation catches every
// scenario that matters with zero deps and identical behaviour
// across operating systems.
//
// Misses precisely-timed wakes where the host has been suspended
// for less than 2× the interval. With interval=30s that means we
// miss a 50-second sleep but catch any 61-second-or-longer one — a
// trade we accept because real-world laptop suspends are minutes
// to hours.
func StartSleepDetector(ctx context.Context, interval time.Duration) <-chan SleepEvent {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	ch := make(chan SleepEvent, 4)
	go func() {
		defer ticker.Stop()
		runSleepDetector(ctx, ticker.C, 2*interval, ch)
	}()
	return ch
}

// runSleepDetector is the inner loop, parameterised on the tick
// source and the gap threshold so tests can inject a synthetic
// channel without spinning a real time.Ticker. Returning the
// detection logic from a real Ticker would tie test latency to
// wall-clock time and make wake detection effectively unverifiable
// in CI (the host OS doesn't suspend its own test runner).
//
// The first tick is recorded as the baseline but does NOT count as
// a gap — there's no previous tick to subtract from.
func runSleepDetector(ctx context.Context, ticks <-chan time.Time, threshold time.Duration, ch chan<- SleepEvent) {
	defer close(ch)
	var last time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case now, ok := <-ticks:
			if !ok {
				return
			}
			if last.IsZero() {
				last = now
				continue
			}
			gap := now.Sub(last)
			last = now
			if gap < threshold {
				continue
			}
			// Drop rather than block. If reconcile is so backed up
			// that 4 wake events have queued without being drained,
			// it'll see one of them when it catches up; signalling
			// a wake twice is harmless (rescans are idempotent).
			select {
			case ch <- SleepEvent{DetectedAt: now, Gap: gap}:
			default:
			}
		}
	}
}

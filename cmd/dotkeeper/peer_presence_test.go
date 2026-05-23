// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// stubPresenceQuerier implements presenceQuerier with controllable
// responses and a call count. The test reaches in to drive timing
// without needing actual Syncthing.
type stubPresenceQuerier struct {
	mu       sync.Mutex
	calls    int
	conns    *stclient.Connections
	connsErr error
}

func (s *stubPresenceQuerier) GetConnections() (*stclient.Connections, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.connsErr != nil {
		return nil, s.connsErr
	}
	return s.conns, nil
}

func (s *stubPresenceQuerier) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func presenceTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopPropWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

// TestPeerPresenceTrackerWritesConnectedPeers — the happy path.
// Tracker queries Syncthing, sees one connected peer, persists
// the timestamp to state.LastSeenPeers, and a subsequent
// LoadStateV2 reads it back.
func TestPeerPresenceTrackerWritesConnectedPeers(t *testing.T) {
	// Isolate XDG so we don't touch real state.toml.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	const liveID = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	const offlineID = "ZZZZZZZ-YYYYYYY-XXXXXXX-WWWWWWW-VVVVVVV-UUUUUUU-TTTTTTT-SSSSSSS"
	stub := &stubPresenceQuerier{conns: &stclient.Connections{
		Connections: map[string]stclient.Connection{
			liveID:    {Connected: true},
			offlineID: {Connected: false},
		},
	}}

	// Short interval so the initial-tick update fires fast.
	// runPeerPresenceTracker triggers `update()` once
	// synchronously before entering the select loop, so even
	// with a long interval the first call lands.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runPeerPresenceTracker(ctx, stub, time.Hour, presenceTestLogger())
		close(done)
	}()

	// Poll for the first write — sub-second on any reasonable
	// machine. Bounded so test failure surfaces fast rather than
	// hanging.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stub.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stub.callCount() < 1 {
		t.Fatalf("tracker never called GetConnections after 2s")
	}

	// Allow the MutateStateV2 write to land (it runs
	// synchronously after GetConnections returns, but the test's
	// observation of callCount races slightly ahead of the
	// state-file flush). Read with a short retry.
	var st *config.StateV2
	for time.Now().Before(deadline) {
		st, _ = config.LoadStateV2()
		if st != nil && len(st.LastSeenPeers) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if st == nil {
		t.Fatal("state.toml not written")
	}
	if _, ok := st.LastSeenPeers[liveID]; !ok {
		t.Errorf("LastSeenPeers missing connected peer %s; got keys=%v",
			liveID, mapKeys(st.LastSeenPeers))
	}
	if _, ok := st.LastSeenPeers[offlineID]; ok {
		t.Error("LastSeenPeers contains offline peer; tracker must skip Connected=false entries")
	}
}

// TestPeerPresenceTrackerSurvivesAPIError — when GetConnections
// fails (Syncthing API down, transient network), the tracker
// must NOT panic, must NOT corrupt state, and must continue on
// the next tick. We pin this because the daemon's reconcile
// cycle keeps running through Syncthing flakes; the tracker
// should too.
func TestPeerPresenceTrackerSurvivesAPIError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stub := &stubPresenceQuerier{connsErr: errors.New("syncthing API unreachable")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runPeerPresenceTracker(ctx, stub, time.Hour, presenceTestLogger())
		close(done)
	}()

	// Wait for the first attempt.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stub.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if stub.callCount() < 1 {
		t.Fatal("tracker never called GetConnections")
	}
	// state.toml should NOT have been written. Loading it returns
	// nil/nil when the file is absent.
	st, err := config.LoadStateV2()
	if err != nil {
		t.Errorf("LoadStateV2: %v", err)
	}
	if st != nil && len(st.LastSeenPeers) != 0 {
		t.Errorf("LastSeenPeers should be empty after API error; got %v",
			st.LastSeenPeers)
	}
}

// TestPeerPresenceTrackerHonoursContext — cancelling ctx returns
// from runPeerPresenceTracker promptly so the daemon's shutdown
// path doesn't hang waiting for the next tick.
func TestPeerPresenceTrackerHonoursContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stub := &stubPresenceQuerier{conns: &stclient.Connections{
		Connections: map[string]stclient.Connection{},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Use a long interval so the goroutine spends most of
		// its time blocked in the select.
		runPeerPresenceTracker(ctx, stub, time.Hour, presenceTestLogger())
		close(done)
	}()

	// Give the initial-tick update time to land.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Returned promptly — good.
	case <-time.After(2 * time.Second):
		t.Fatal("runPeerPresenceTracker did not return within 2s of ctx cancellation")
	}
}

func mapKeys(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

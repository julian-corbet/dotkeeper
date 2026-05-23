// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// Tests for `dotkeeper health`. Each one isolates XDG state so
// the suite doesn't read the developer's real state.toml /
// machine.toml.

func setupHealthFixture(t *testing.T) (stateDir, configDir string) {
	t.Helper()
	cfg := t.TempDir()
	st := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", st)
	// Default to "no live data" so tests that don't care about
	// the live-connections path see consistent state. Tests that
	// DO want live data override liveConnectionsProvider after
	// calling setupHealthFixture.
	old := liveConnectionsProvider
	liveConnectionsProvider = func() (map[string]time.Time, error) { return nil, nil }
	t.Cleanup(func() { liveConnectionsProvider = old })
	return st, cfg
}

func writeMachineToml(t *testing.T, name string, peers []config.PeerEntry) {
	t.Helper()
	m := &config.MachineConfigV2{
		SchemaVersion: 2,
		Name:          name,
		Slot:          1,
		Discovery: config.DiscoveryConfig{
			ScanRoots: []string{"~/Documents/GitHub"},
			ScanDepth: 3,
		},
		Peers: peers,
	}
	if err := config.WriteMachineConfigV2(m); err != nil {
		t.Fatalf("WriteMachineConfigV2: %v", err)
	}
}

func writeStateToml(t *testing.T, s *config.StateV2) {
	t.Helper()
	if err := config.WriteStateV2(s); err != nil {
		t.Fatalf("WriteStateV2: %v", err)
	}
}

func TestHealthReportRepoAgeBuckets(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "test-host", nil)

	now := time.Now()
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			"/repo/fresh":      {LastBackupAt: now.Add(-1 * time.Hour)},
			"/repo/stale1":     {LastBackupAt: now.Add(-2 * 24 * time.Hour)},
			"/repo/stale2":     {LastBackupAt: now.Add(-5 * 24 * time.Hour)},
			"/repo/very-stale": {LastBackupAt: now.Add(-10 * 24 * time.Hour)},
			"/repo/never":      {LastBackupAt: time.Time{}},
		},
	})

	r, err := collectHealth(true) // noLogScan: skip log to keep test deterministic
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}

	if r.Repos.Total != 5 {
		t.Errorf("Total=%d, want 5", r.Repos.Total)
	}
	if r.Repos.FreshLast24h != 1 {
		t.Errorf("FreshLast24h=%d, want 1", r.Repos.FreshLast24h)
	}
	if r.Repos.StaleOneToSeven != 2 {
		t.Errorf("StaleOneToSeven=%d, want 2", r.Repos.StaleOneToSeven)
	}
	if r.Repos.StaleOverSeven != 1 {
		t.Errorf("StaleOverSeven=%d, want 1", r.Repos.StaleOverSeven)
	}
	if r.Repos.NeverBackedUp != 1 {
		t.Errorf("NeverBackedUp=%d, want 1", r.Repos.NeverBackedUp)
	}

	// degraded() must report true because NeverBackedUp > 0 AND
	// StaleOverSeven > 0.
	if !r.degraded() {
		t.Error("report should be degraded with stale+never-backed-up repos")
	}
}

func TestHealthReportPeersMergeMachineAndState(t *testing.T) {
	setupHealthFixture(t)

	const seenID = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	const unseenID = "ZZZZZZZ-YYYYYYY-XXXXXXX-WWWWWWW-VVVVVVV-UUUUUUU-TTTTTTT-SSSSSSS"

	writeMachineToml(t, "host", []config.PeerEntry{
		{Name: "alpha", DeviceID: seenID, LearnedAt: time.Now()},
		{Name: "beta", DeviceID: unseenID, LearnedAt: time.Now()},
	})

	when := time.Now().Add(-3 * time.Hour)
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		LastSeenPeers: map[string]time.Time{
			seenID: when,
		},
	})

	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.Peers.Known != 2 {
		t.Fatalf("Peers.Known=%d, want 2", r.Peers.Known)
	}
	if len(r.Peers.LastSeen) != 2 {
		t.Fatalf("LastSeen rows=%d, want 2", len(r.Peers.LastSeen))
	}
	// Sorted by name: alpha first.
	if r.Peers.LastSeen[0].Name != "alpha" {
		t.Errorf("first peer = %s, want alpha", r.Peers.LastSeen[0].Name)
	}
	if r.Peers.LastSeen[0].Since.IsZero() {
		t.Error("alpha should have a non-zero last-seen timestamp")
	}
	if !r.Peers.LastSeen[1].Since.IsZero() {
		t.Error("beta should have zero last-seen (never observed)")
	}
}

func TestHealthHealthyReportNotDegraded(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			"/repo/a": {LastBackupAt: time.Now().Add(-1 * time.Hour)},
		},
	})
	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.degraded() {
		t.Errorf("healthy report flagged degraded: %+v", r.Repos)
	}
}

func TestHealthJSONIsStable(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			"/repo/a": {LastBackupAt: time.Now().Add(-1 * time.Hour)},
		},
	})
	r, _ := collectHealth(true)
	var buf bytes.Buffer
	if err := writeHealthJSON(&buf, r); err != nil {
		t.Fatalf("writeHealthJSON: %v", err)
	}
	// Sanity-check that the wire format uses kebab-case keys as
	// documented in the HealthReport struct comment — these are
	// API surface and must not silently drift to snake_case.
	out := buf.String()
	for _, k := range []string{
		`"generated-at"`,
		`"fresh-last-24h"`,
		`"stale-1-to-7-days"`,
		`"oldest-backups"`,
	} {
		if !strings.Contains(out, k) {
			t.Errorf("JSON output missing stable key %s in:\n%s", k, out)
		}
	}
	// Round-trip must decode without error.
	var dec HealthReport
	if err := json.Unmarshal(buf.Bytes(), &dec); err != nil {
		t.Errorf("JSON round-trip failed: %v", err)
	}
}

func TestHealthScansRecentActivityFromLog(t *testing.T) {
	stateDir, _ := setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	now := time.Now()
	recent := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	stale := now.Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	logBody := strings.Join([]string{
		`time=` + recent + ` level=WARN msg="propagator: push failed" peer=desktop`,
		`time=` + recent + ` level=ERROR msg="something broke"`,
		`time=` + recent + ` level=INFO msg="auto: resolve sync conflict in foo.txt"`,
		`time=` + stale + ` level=ERROR msg="ancient error outside window"`,
		`unparseable line that should be skipped`,
		``,
	}, "\n")
	logPath := filepath.Join(stateDir, "dotkeeper", "syncthing.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := collectHealth(false)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.RecentActivity == nil {
		t.Fatal("RecentActivity is nil; log scan should have populated it")
	}
	if r.RecentActivity.PushFailures != 1 {
		t.Errorf("PushFailures=%d, want 1", r.RecentActivity.PushFailures)
	}
	if r.RecentActivity.ErrorCount != 1 {
		t.Errorf("ErrorCount=%d, want 1 (the stale ERROR must be filtered out)", r.RecentActivity.ErrorCount)
	}
	if r.RecentActivity.ConflictResolved != 1 {
		t.Errorf("ConflictResolved=%d, want 1", r.RecentActivity.ConflictResolved)
	}
}

// TestHealthPrefersLiveConnectionsOverStateCache pins the priority
// order in summarisePeers: a peer that Syncthing reports as
// currently connected should show a fresh timestamp even if
// state.LastSeenPeers has an older one. The "live > cache" rule
// is the whole reason for the indirection through
// liveConnectionsProvider.
func TestHealthPrefersLiveConnectionsOverStateCache(t *testing.T) {
	setupHealthFixture(t)

	const id = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	writeMachineToml(t, "host", []config.PeerEntry{
		{Name: "live-peer", DeviceID: id, LearnedAt: time.Now()},
	})

	// Cache has a 24h-old timestamp; live says "connected now".
	staleCache := time.Now().Add(-24 * time.Hour)
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		LastSeenPeers: map[string]time.Time{id: staleCache},
	})

	freshLive := time.Now()
	liveConnectionsProvider = func() (map[string]time.Time, error) {
		return map[string]time.Time{id: freshLive}, nil
	}

	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if len(r.Peers.LastSeen) != 1 {
		t.Fatalf("expected 1 peer row, got %d", len(r.Peers.LastSeen))
	}
	if r.Peers.LastSeen[0].Since.Before(staleCache.Add(time.Hour)) {
		t.Errorf("expected live timestamp (~now), got %v which is no fresher than the 24h-old cache",
			r.Peers.LastSeen[0].Since)
	}
}

// TestHealthFallsBackToCacheWhenLiveUnavailable — opposite of the
// previous: live provider returns nil (Syncthing unreachable); the
// command must still report the cached last-seen.
func TestHealthFallsBackToCacheWhenLiveUnavailable(t *testing.T) {
	setupHealthFixture(t)

	const id = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	writeMachineToml(t, "host", []config.PeerEntry{
		{Name: "cached-peer", DeviceID: id, LearnedAt: time.Now()},
	})
	cached := time.Now().Add(-2 * time.Hour)
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		LastSeenPeers: map[string]time.Time{id: cached},
	})
	// liveConnectionsProvider is the setupHealthFixture default
	// (returns nil, nil). Don't override.

	r, _ := collectHealth(true)
	if len(r.Peers.LastSeen) != 1 {
		t.Fatalf("expected 1 peer row, got %d", len(r.Peers.LastSeen))
	}
	// TOML datetime serialisation drops sub-second precision, so
	// compare at second resolution. Cross-tz comparison is fine
	// because both sides represent the same absolute instant.
	if !r.Peers.LastSeen[0].Since.Truncate(time.Second).Equal(cached.Truncate(time.Second)) {
		t.Errorf("expected cached timestamp %v, got %v", cached, r.Peers.LastSeen[0].Since)
	}
}

func TestExtractSlogTimestamp(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`time=2026-05-23T19:00:00Z level=INFO msg="hi"`, true},
		{`time=2026-05-23T19:00:00+02:00 level=WARN`, true},
		{`level=ERROR no time`, false},
		{`time=not-a-time level=INFO`, false},
		{``, false},
		{`time=2026-05-23T19:00:00Z`, false}, // no trailing space
	}
	for _, c := range cases {
		_, got := extractSlogTimestamp(c.in)
		if got != c.want {
			t.Errorf("extractSlogTimestamp(%q): got %v want %v", c.in, got, c.want)
		}
	}
}

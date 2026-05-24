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
	oldLive := liveConnectionsProvider
	liveConnectionsProvider = func() (map[string]time.Time, error) { return nil, nil }
	t.Cleanup(func() { liveConnectionsProvider = oldLive })
	// Default gitMTimeProvider returns time.Now() for any path,
	// so the test paths (which don't exist on disk) appear to
	// have FRESH git activity — i.e. any backup older than the
	// laggingGrace window is treated as lagging. This matches
	// the prior test semantics where "old backup" implied
	// "stale". Tests that want different git-activity behaviour
	// override gitMTimeProvider after calling setupHealthFixture.
	oldMTime := gitMTimeProvider
	gitMTimeProvider = func(path string) time.Time { return time.Now() }
	t.Cleanup(func() { gitMTimeProvider = oldMTime })
	// Default daemonProcInfoProvider returns "no daemon running"
	// so tests don't pick up whatever process happens to be on
	// the developer's box. Tests that exercise the daemon-PID
	// path override this.
	oldDaemon := daemonProcInfoProvider
	daemonProcInfoProvider = func() (int, time.Time) { return 0, time.Time{} }
	t.Cleanup(func() { daemonProcInfoProvider = oldDaemon })
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
	// Healthy case: backup is 1h old, git's HEAD is older still —
	// the backup is correctly current relative to git activity.
	// Override the default fresh-git stub so this scenario hits
	// the "idle" bucket rather than "lagging".
	gitMTimeProvider = func(_ string) time.Time {
		return time.Now().Add(-2 * time.Hour)
	}
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

// TestHealthDormantReposAreIdleNotDegraded pins the v1.1.4
// false-positive fix: a repo whose git tree hasn't moved in
// months but whose backup is also old should be classified as
// Idle and NOT trigger a degraded report. Pre-fix this would
// have shown up as "Very stale: N" and pushed the exit code to
// 1, training operators to ignore the signal.
func TestHealthDormantReposAreIdleNotDegraded(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	// Both git and backup are 30 days old — the repo is just dormant.
	gitMTimeProvider = func(_ string) time.Time {
		return time.Now().Add(-30 * 24 * time.Hour)
	}
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			"/repo/dormant": {LastBackupAt: time.Now().Add(-30 * 24 * time.Hour)},
		},
	})
	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.degraded() {
		t.Errorf("dormant repo flagged degraded; should be idle: %+v", r.Repos)
	}
	if r.Repos.Idle != 1 {
		t.Errorf("Idle count = %d, want 1; buckets=%+v", r.Repos.Idle, r.Repos)
	}
	if r.Repos.StaleOverSeven != 0 {
		t.Errorf("dormant repo should not be in StaleOverSeven; got %d", r.Repos.StaleOverSeven)
	}
}

// TestHealthLaggingBackupTriggersDegraded — the opposite case.
// Git has activity newer than the backup → the backup is
// genuinely behind, and degraded() must fire.
func TestHealthLaggingBackupTriggersDegraded(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	// Backup is 2 days old; git activity is 1 hour ago. Lag is
	// real and well past the 10-min grace window.
	gitMTimeProvider = func(_ string) time.Time {
		return time.Now().Add(-1 * time.Hour)
	}
	writeStateToml(t, &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			"/repo/lagging": {LastBackupAt: time.Now().Add(-2 * 24 * time.Hour)},
		},
	})
	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if !r.degraded() {
		t.Errorf("lagging-backup repo should be degraded; got %+v", r.Repos)
	}
	if len(r.Repos.LaggingBackups) != 1 {
		t.Errorf("LaggingBackups count = %d, want 1", len(r.Repos.LaggingBackups))
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

// TestHealthHistoricalErrorsDontDegrade pins the v1.1.5 fix:
// log entries with level=ERROR that fall in the 24h display
// window but are older than 1 hour must contribute to
// ErrorCount (for display) but NOT to ErrorsLastHour (the
// degraded() trigger). Without this distinction, a syncthing.log
// containing pre-fix error noise permanently marks the daemon as
// degraded — exactly the false signal that trained the operator
// to ignore the command.
func TestHealthHistoricalErrorsDontDegrade(t *testing.T) {
	stateDir, _ := setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	now := time.Now()
	oldButInWindow := now.Add(-12 * time.Hour).UTC().Format(time.RFC3339)
	veryRecent := now.Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	logBody := strings.Join([]string{
		// 5 historical errors — should land in ErrorCount but
		// not ErrorsLastHour
		`time=` + oldButInWindow + ` level=ERROR msg="historical noise 1"`,
		`time=` + oldButInWindow + ` level=ERROR msg="historical noise 2"`,
		`time=` + oldButInWindow + ` level=ERROR msg="historical noise 3"`,
		`time=` + oldButInWindow + ` level=ERROR msg="historical noise 4"`,
		`time=` + oldButInWindow + ` level=ERROR msg="historical noise 5"`,
		// No recent errors — the daemon's current state is clean
		`time=` + veryRecent + ` level=INFO msg="reconcile triggered"`,
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
		t.Fatal("RecentActivity nil; log scan must have populated it")
	}
	if r.RecentActivity.ErrorCount != 5 {
		t.Errorf("ErrorCount = %d, want 5 (all historical errors counted for display)",
			r.RecentActivity.ErrorCount)
	}
	if r.RecentActivity.ErrorsLastHour != 0 {
		t.Errorf("ErrorsLastHour = %d, want 0 (historical errors must not count toward the degraded trigger)",
			r.RecentActivity.ErrorsLastHour)
	}
	if r.degraded() {
		t.Errorf("degraded must NOT fire when all errors are historical; report=%+v", r.RecentActivity)
	}
}

// TestHealthRecentErrorsDoDegrade — opposite case. An error
// within the last hour MUST trigger degraded.
func TestHealthRecentErrorsDoDegrade(t *testing.T) {
	stateDir, _ := setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	veryRecent := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	logBody := `time=` + veryRecent + ` level=ERROR msg="actual current problem"` + "\n"
	logPath := filepath.Join(stateDir, "dotkeeper", "syncthing.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := collectHealth(false)
	if r.RecentActivity.ErrorsLastHour != 1 {
		t.Errorf("ErrorsLastHour = %d, want 1", r.RecentActivity.ErrorsLastHour)
	}
	if !r.degraded() {
		t.Errorf("degraded must fire on recent error; report=%+v", r.RecentActivity)
	}
}

// TestHealthReportIncludesBuildAndDaemonInfo pins the v1.1.8
// addition: every HealthReport carries the binary's version +
// commit and the running daemon's PID + start time. Lets
// downstream alerting correlate a "ErrorsLastHour=N" event
// against a specific build, and shows daemon-up vs daemon-down
// at-a-glance.
func TestHealthReportIncludesBuildAndDaemonInfo(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	// Stub the daemon-info provider with a known PID + start time.
	wantPID := 12345
	wantStart := time.Now().Add(-30 * time.Minute)
	daemonProcInfoProvider = func() (int, time.Time) { return wantPID, wantStart }

	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.Build.Version == "" {
		t.Error("Build.Version is empty; should reflect compiled-in version")
	}
	if r.DaemonPID != wantPID {
		t.Errorf("DaemonPID = %d, want %d", r.DaemonPID, wantPID)
	}
	if !r.DaemonStartedAt.Equal(wantStart) {
		t.Errorf("DaemonStartedAt = %v, want %v", r.DaemonStartedAt, wantStart)
	}
}

// TestHealthReportHandlesNoDaemon — when no `dotkeeper start`
// process is running, the report still produces useful output:
// PID is zero, StartedAt is zero, text rendering says "not
// running". The command must work during outages — that's
// precisely when it's most needed.
func TestHealthReportHandlesNoDaemon(t *testing.T) {
	setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})
	// daemonProcInfoProvider already stubbed to return 0/zero
	// by setupHealthFixture.

	r, err := collectHealth(true)
	if err != nil {
		t.Fatalf("collectHealth: %v", err)
	}
	if r.DaemonPID != 0 {
		t.Errorf("DaemonPID = %d, want 0 (no daemon)", r.DaemonPID)
	}
	if !r.DaemonStartedAt.IsZero() {
		t.Errorf("DaemonStartedAt = %v, want zero time", r.DaemonStartedAt)
	}

	var buf bytes.Buffer
	writeHealthText(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "not running") {
		t.Errorf("text output should say 'not running' when no daemon; got:\n%s", out)
	}
}

// TestHealthBreaksDownWarningsByKind pins the v1.1.6 feature:
// the warning total is supplemented with a top-N breakdown by
// message kind, so 360 warnings dominated by one message kind
// is visibly different from 360 distinct problems.
func TestHealthBreaksDownWarningsByKind(t *testing.T) {
	stateDir, _ := setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	ts := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	var lines []string
	// 5 of "common", 3 of "less common", 1 of "rare"
	for i := 0; i < 5; i++ {
		lines = append(lines, `time=`+ts+` level=WARN msg="common kind"`)
	}
	for i := 0; i < 3; i++ {
		lines = append(lines, `time=`+ts+` level=WARN msg="less common kind"`)
	}
	lines = append(lines, `time=`+ts+` level=WARN msg="rare kind"`)
	lines = append(lines, ``)
	logPath := filepath.Join(stateDir, "dotkeeper", "syncthing.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := collectHealth(false)
	if r.RecentActivity.WarnCount != 9 {
		t.Errorf("WarnCount = %d, want 9", r.RecentActivity.WarnCount)
	}
	if len(r.RecentActivity.TopWarningKinds) != 3 {
		t.Fatalf("TopWarningKinds len = %d, want 3; got %v",
			len(r.RecentActivity.TopWarningKinds), r.RecentActivity.TopWarningKinds)
	}
	// Most common first.
	if r.RecentActivity.TopWarningKinds[0].Message != "common kind" ||
		r.RecentActivity.TopWarningKinds[0].Count != 5 {
		t.Errorf("top kind = %+v, want {common kind, 5}", r.RecentActivity.TopWarningKinds[0])
	}
	if r.RecentActivity.TopWarningKinds[1].Message != "less common kind" {
		t.Errorf("second = %s, want less common kind", r.RecentActivity.TopWarningKinds[1].Message)
	}
}

// TestHealthTopWarningKindsTracksLastHourSeparately pins the
// v1.1.7 refinement: each warning-kind row carries both a
// 24h total AND a last-hour subset. Lets operators distinguish
// chronic historical warnings (24h count high, last-hour count
// zero — old residue, ignore) from currently-flapping ones
// (last-hour count > 0 — investigate now).
func TestHealthTopWarningKindsTracksLastHourSeparately(t *testing.T) {
	stateDir, _ := setupHealthFixture(t)
	writeMachineToml(t, "host", nil)
	writeStateToml(t, &config.StateV2{SchemaVersion: 2})

	now := time.Now()
	stale := now.Add(-6 * time.Hour).UTC().Format(time.RFC3339)
	recent := now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	var lines []string
	// 10 of "chronic" all in stale window
	for i := 0; i < 10; i++ {
		lines = append(lines, `time=`+stale+` level=WARN msg="chronic"`)
	}
	// 3 of "flapping" all in recent window
	for i := 0; i < 3; i++ {
		lines = append(lines, `time=`+recent+` level=WARN msg="flapping"`)
	}
	lines = append(lines, ``)
	logPath := filepath.Join(stateDir, "dotkeeper", "syncthing.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := collectHealth(false)
	if len(r.RecentActivity.TopWarningKinds) != 2 {
		t.Fatalf("TopWarningKinds len = %d, want 2", len(r.RecentActivity.TopWarningKinds))
	}
	// Sorted by Count desc: chronic (10) > flapping (3).
	chronic := r.RecentActivity.TopWarningKinds[0]
	flapping := r.RecentActivity.TopWarningKinds[1]
	if chronic.Message != "chronic" || chronic.Count != 10 || chronic.CountLastHour != 0 {
		t.Errorf("chronic row = %+v, want {chronic, 10, 0}", chronic)
	}
	if flapping.Message != "flapping" || flapping.Count != 3 || flapping.CountLastHour != 3 {
		t.Errorf("flapping row = %+v, want {flapping, 3, 3}", flapping)
	}
}

// TestHealthDegradedReasonsEnumerates pins the v1.1.10 feature:
// degradedReasons() returns one string per triggering
// condition, in the operationally-most-actionable-first order
// the text renderer relies on.
func TestHealthDegradedReasonsEnumerates(t *testing.T) {
	r := &HealthReport{}
	r.Repos.LaggingBackups = []RepoLaggingBackup{
		{Path: "/repo/a", LagSeconds: 3600},
		{Path: "/repo/b", LagSeconds: 7200},
	}
	r.Repos.NeverBackedUp = 1
	r.RecentActivity = &RecentActivity{
		ErrorsLastHour: 5,
		PushFailures:   2,
	}

	reasons := r.degradedReasons()
	if len(reasons) != 4 {
		t.Fatalf("got %d reasons, want 4; got=%v", len(reasons), reasons)
	}
	// Order: errors-last-hour → push-failures → lagging → never.
	wantPrefixes := []string{
		"5 ERROR-level",
		"2 propagator push failure",
		"2 repo(s) with git activity",
		"1 repo(s) tracked but never",
	}
	for i, want := range wantPrefixes {
		if !strings.HasPrefix(reasons[i], want) {
			t.Errorf("reason[%d] = %q, want prefix %q", i, reasons[i], want)
		}
	}
}

// TestHealthDegradedReasonsEmptyWhenHealthy — a clean report
// produces zero reasons, so degraded() returns false and the
// text renderer falls into the "healthy" branch.
func TestHealthDegradedReasonsEmptyWhenHealthy(t *testing.T) {
	r := &HealthReport{} // all fields zero
	if got := r.degradedReasons(); len(got) != 0 {
		t.Errorf("healthy report should yield 0 reasons; got %v", got)
	}
	if r.degraded() {
		t.Error("healthy report should not be degraded")
	}
}

// TestHealthTextOutputShowsDegradedReasons — the rendered text
// includes the "degraded because:" footer with one bullet per
// reason. Operators read this to triage without scrolling.
func TestHealthTextOutputShowsDegradedReasons(t *testing.T) {
	r := &HealthReport{}
	r.Repos.LaggingBackups = []RepoLaggingBackup{{Path: "/repo/x", LagSeconds: 100}}
	var buf bytes.Buffer
	writeHealthText(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "degraded because:") {
		t.Errorf("text output missing 'degraded because:' footer; got:\n%s", out)
	}
	if !strings.Contains(out, "1 repo(s) with git activity newer than the last backup") {
		t.Errorf("text output missing specific lagging-backup reason; got:\n%s", out)
	}
}

// TestHealthExplainRendersKnownPatterns pins the v1.1.9
// --explain feature: for each warning kind in the report whose
// message matches one of the knownPatternExplanations, the
// explainer renders a "what this means / what to do" line.
// Unknown patterns are silently skipped.
func TestHealthExplainRendersKnownPatterns(t *testing.T) {
	r := &HealthReport{
		RecentActivity: &RecentActivity{
			TopWarningKinds: []WarningKind{
				{Message: "Unexpected folder ID in ClusterConfig; etc etc"},
				{Message: "Something totally unknown"},
				{Message: "Detected a flip-flopping listener server=https://x"},
			},
		},
	}
	var buf bytes.Buffer
	writeHealthExplanations(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "A peer is offering a folder") {
		t.Errorf("explain output missing folder-ID-in-ClusterConfig advice; got:\n%s", out)
	}
	if !strings.Contains(out, "flip-flopping") || !strings.Contains(out, "NAT/firewall") {
		t.Errorf("explain output missing flip-flopping advice; got:\n%s", out)
	}
	if strings.Contains(out, "Something totally unknown") {
		t.Errorf("unknown pattern should be silently skipped; got:\n%s", out)
	}
}

// TestHealthExplainSilentWhenNothingRecognised — when no
// warning kinds match the known patterns, the explain section
// must produce ZERO output (not even the heading). The mode is
// opt-in help; an empty heading would be visual noise.
func TestHealthExplainSilentWhenNothingRecognised(t *testing.T) {
	r := &HealthReport{
		RecentActivity: &RecentActivity{
			TopWarningKinds: []WarningKind{
				{Message: "Some random message not in the table"},
			},
		},
	}
	var buf bytes.Buffer
	writeHealthExplanations(&buf, r)
	if buf.Len() != 0 {
		t.Errorf("explain should produce no output when no patterns match; got:\n%s", buf.String())
	}
}

// TestHealthExplainSilentWhenNoActivity — when RecentActivity
// is nil (e.g. --no-log-scan was used), explain must not panic
// or render anything.
func TestHealthExplainSilentWhenNoActivity(t *testing.T) {
	r := &HealthReport{RecentActivity: nil}
	var buf bytes.Buffer
	writeHealthExplanations(&buf, r)
	if buf.Len() != 0 {
		t.Errorf("explain should produce no output for nil RecentActivity; got:\n%s", buf.String())
	}
}

// TestWrapForExplain — the explainer wraps long lines for
// readability while keeping words atomic.
func TestWrapForExplain(t *testing.T) {
	got := wrapForExplain("one two three four five six seven eight nine ten", 20)
	// Each line ≤ 20 chars (after the implicit 4-char indent on
	// continuations), no mid-word break.
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(trimmed) > 20 {
			t.Errorf("line exceeds 20-char width: %q (%d chars)", trimmed, len(trimmed))
		}
	}
}

// TestExtractMsgField — slog text-handler messages may contain
// equals signs, embedded quotes, etc.; pin the parser against
// the realistic shapes.
func TestExtractMsgField(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`time=2026-05-24T01:00:00Z level=WARN msg="simple message"`, "simple message"},
		{`time=2026-05-24T01:00:00Z level=WARN msg="with key=value inside" extra=1`, "with key=value inside"},
		{`time=2026-05-24T01:00:00Z level=ERROR msg="" path=/x`, ""},
		{`time=2026-05-24T01:00:00Z level=INFO no_msg=here`, ""},
		{`raw text with no slog framing`, ""},
	}
	for _, c := range cases {
		got := extractMsgField(c.in)
		if got != c.want {
			t.Errorf("extractMsgField(%q) = %q, want %q", c.in, got, c.want)
		}
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

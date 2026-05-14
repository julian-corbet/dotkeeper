// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"testing"
	"time"
)

// TestParseGitInterval exhaustively covers the keyword aliases and the
// time.ParseDuration fallback, plus invalid input returning zero. These
// branches were previously at 22.2% coverage; without them, regressions
// in the cron string parser would slip through silently.
func TestParseGitInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want time.Duration
	}{
		// Keywords (the documented sugar).
		{"", time.Hour},
		{"hourly", time.Hour},
		{"daily", 24 * time.Hour},
		{"weekly", 7 * 24 * time.Hour},
		{"monthly", 30 * 24 * time.Hour},
		// time.ParseDuration fallback.
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"2h30m", 2*time.Hour + 30*time.Minute},
		{"1h0m0s", time.Hour},
		// Garbage → 0 (caller substitutes a default).
		{"banana", 0},
		{"forever", 0},
		{"1 week", 0},     // space-separated, ParseDuration rejects
		{"yearly", 0},     // not a documented keyword
		{"-30s", -30 * time.Second}, // ParseDuration accepts negatives; caller's `interval <= 0` guard catches it
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := parseGitInterval(tc.in)
			if got != tc.want {
				t.Errorf("parseGitInterval(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRepoBackupDue covers every branch of the scheduling decision: each
// CommitPolicy ("on-idle", "timer", anything else → never), each variant of
// the observed state (dirty vs clean, backed-up recently vs never, idle
// window expired vs not). Previously at 37.5% — what was missed was mostly
// the "timer never backed up" and "on-idle no LastChangeAt" branches.
func TestRepoBackupDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		desired  RepoDesired
		observed RepoObs
		want     bool
	}{
		// --- on-idle policy -------------------------------------------------
		{
			name:     "on-idle: clean repo, always due",
			desired:  RepoDesired{CommitPolicy: "on-idle"},
			observed: RepoObs{IsDirty: false},
			want:     true,
		},
		{
			name:     "on-idle: dirty with no LastChangeAt → due (treat as ancient)",
			desired:  RepoDesired{CommitPolicy: "on-idle"},
			observed: RepoObs{IsDirty: true},
			want:     true,
		},
		{
			name:     "on-idle: dirty, last change inside default 5m window → NOT due",
			desired:  RepoDesired{CommitPolicy: "on-idle"},
			observed: RepoObs{IsDirty: true, LastChangeAt: now.Add(-2 * time.Minute)},
			want:     false,
		},
		{
			name:     "on-idle: dirty, last change outside default 5m window → due",
			desired:  RepoDesired{CommitPolicy: "on-idle"},
			observed: RepoObs{IsDirty: true, LastChangeAt: now.Add(-10 * time.Minute)},
			want:     true,
		},
		{
			name:     "on-idle: custom IdleSeconds=30, change 1m ago → due",
			desired:  RepoDesired{CommitPolicy: "on-idle", IdleSeconds: 30},
			observed: RepoObs{IsDirty: true, LastChangeAt: now.Add(-1 * time.Minute)},
			want:     true,
		},
		{
			name:     "on-idle: custom IdleSeconds=600, change 5m ago → NOT due",
			desired:  RepoDesired{CommitPolicy: "on-idle", IdleSeconds: 600},
			observed: RepoObs{IsDirty: true, LastChangeAt: now.Add(-5 * time.Minute)},
			want:     false,
		},
		// --- timer policy ---------------------------------------------------
		{
			name:     "timer: never backed up → due",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "hourly"},
			observed: RepoObs{},
			want:     true,
		},
		{
			name:     "timer: hourly, backed up 30m ago → NOT due",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "hourly"},
			observed: RepoObs{LastBackupAt: now.Add(-30 * time.Minute)},
			want:     false,
		},
		{
			name:     "timer: hourly, backed up 2h ago → due",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "hourly"},
			observed: RepoObs{LastBackupAt: now.Add(-2 * time.Hour)},
			want:     true,
		},
		{
			name:     "timer: daily, backed up 25h ago → due",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "daily"},
			observed: RepoObs{LastBackupAt: now.Add(-25 * time.Hour)},
			want:     true,
		},
		{
			name:     "timer: invalid interval falls back to 1h default → due after 90m",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "banana"},
			observed: RepoObs{LastBackupAt: now.Add(-90 * time.Minute)},
			want:     true,
		},
		{
			name:     "timer: invalid interval falls back to 1h default → NOT due after 30m",
			desired:  RepoDesired{CommitPolicy: "timer", GitInterval: "banana"},
			observed: RepoObs{LastBackupAt: now.Add(-30 * time.Minute)},
			want:     false,
		},
		// --- unknown / default policy --------------------------------------
		{
			name:     "unknown policy: never due",
			desired:  RepoDesired{CommitPolicy: "manual"},
			observed: RepoObs{IsDirty: true, LastChangeAt: now.Add(-24 * time.Hour)},
			want:     false,
		},
		{
			name:     "empty policy: never due",
			desired:  RepoDesired{CommitPolicy: ""},
			observed: RepoObs{},
			want:     false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := repoBackupDue(tc.desired, tc.observed, now); got != tc.want {
				t.Errorf("repoBackupDue = %v, want %v", got, tc.want)
			}
		})
	}
}

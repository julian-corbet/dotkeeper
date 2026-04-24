// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- MachineConfigV2 tests ---

func TestMachineV2Defaults(t *testing.T) {
	cfg := &MachineConfigV2{}
	applyMachineV2Defaults(cfg)

	if cfg.DefaultCommitPolicy != "manual" {
		t.Errorf("DefaultCommitPolicy = %q, want %q", cfg.DefaultCommitPolicy, "manual")
	}
	if cfg.DefaultGitInterval != "hourly" {
		t.Errorf("DefaultGitInterval = %q, want %q", cfg.DefaultGitInterval, "hourly")
	}
	if cfg.DefaultSlotOffsetMinutes != 5 {
		t.Errorf("DefaultSlotOffsetMinutes = %d, want 5", cfg.DefaultSlotOffsetMinutes)
	}
	if cfg.ReconcileInterval != "5m" {
		t.Errorf("ReconcileInterval = %q, want %q", cfg.ReconcileInterval, "5m")
	}
	if len(cfg.Discovery.ScanRoots) != 2 {
		t.Errorf("ScanRoots has %d entries, want 2", len(cfg.Discovery.ScanRoots))
	}
	if cfg.Discovery.ScanInterval != "5m" {
		t.Errorf("ScanInterval = %q, want %q", cfg.Discovery.ScanInterval, "5m")
	}
	if cfg.Discovery.ScanDepth != 3 {
		t.Errorf("ScanDepth = %d, want 3", cfg.Discovery.ScanDepth)
	}
}

func TestMachineV2DefaultsPreserveExplicit(t *testing.T) {
	// Values explicitly set should not be overwritten by defaults.
	cfg := &MachineConfigV2{
		DefaultCommitPolicy:      "timer",
		DefaultGitInterval:       "daily",
		DefaultSlotOffsetMinutes: 10,
		ReconcileInterval:        "10m",
		Discovery: DiscoveryConfig{
			ScanRoots:    []string{"~/Work"},
			ScanInterval: "15m",
			ScanDepth:    5,
		},
	}
	applyMachineV2Defaults(cfg)

	if cfg.DefaultCommitPolicy != "timer" {
		t.Errorf("DefaultCommitPolicy overwritten, got %q", cfg.DefaultCommitPolicy)
	}
	if cfg.DefaultGitInterval != "daily" {
		t.Errorf("DefaultGitInterval overwritten, got %q", cfg.DefaultGitInterval)
	}
	if cfg.DefaultSlotOffsetMinutes != 10 {
		t.Errorf("DefaultSlotOffsetMinutes overwritten, got %d", cfg.DefaultSlotOffsetMinutes)
	}
	if cfg.ReconcileInterval != "10m" {
		t.Errorf("ReconcileInterval overwritten, got %q", cfg.ReconcileInterval)
	}
	if len(cfg.Discovery.ScanRoots) != 1 || cfg.Discovery.ScanRoots[0] != "~/Work" {
		t.Errorf("ScanRoots overwritten, got %v", cfg.Discovery.ScanRoots)
	}
	if cfg.Discovery.ScanInterval != "15m" {
		t.Errorf("ScanInterval overwritten, got %q", cfg.Discovery.ScanInterval)
	}
	if cfg.Discovery.ScanDepth != 5 {
		t.Errorf("ScanDepth overwritten, got %d", cfg.Discovery.ScanDepth)
	}
}

func TestMachineV2RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	original := &MachineConfigV2{
		SchemaVersion:            2,
		Name:                     "test-desktop",
		Slot:                     1,
		DefaultCommitPolicy:      "on-idle",
		DefaultGitInterval:       "daily",
		DefaultSlotOffsetMinutes: 7,
		DefaultShareWith:         []string{"elitebook", "server"},
		ReconcileInterval:        "3m",
		Discovery: DiscoveryConfig{
			ScanRoots:    []string{"~/Documents/GitHub", "~/Projects"},
			Exclude:      []string{"~/Documents/GitHub/some-fork"},
			ScanInterval: "10m",
			ScanDepth:    4,
		},
	}

	if err := WriteMachineConfigV2(original); err != nil {
		t.Fatalf("WriteMachineConfigV2: %v", err)
	}

	loaded, err := LoadMachineConfigV2()
	if err != nil {
		t.Fatalf("LoadMachineConfigV2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMachineConfigV2 returned nil")
	}

	if loaded.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", loaded.SchemaVersion, original.SchemaVersion)
	}
	if loaded.Name != original.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Slot != original.Slot {
		t.Errorf("Slot = %d, want %d", loaded.Slot, original.Slot)
	}
	if loaded.DefaultCommitPolicy != original.DefaultCommitPolicy {
		t.Errorf("DefaultCommitPolicy = %q, want %q", loaded.DefaultCommitPolicy, original.DefaultCommitPolicy)
	}
	if loaded.DefaultGitInterval != original.DefaultGitInterval {
		t.Errorf("DefaultGitInterval = %q, want %q", loaded.DefaultGitInterval, original.DefaultGitInterval)
	}
	if loaded.DefaultSlotOffsetMinutes != original.DefaultSlotOffsetMinutes {
		t.Errorf("DefaultSlotOffsetMinutes = %d, want %d", loaded.DefaultSlotOffsetMinutes, original.DefaultSlotOffsetMinutes)
	}
	if len(loaded.DefaultShareWith) != len(original.DefaultShareWith) {
		t.Errorf("DefaultShareWith len = %d, want %d", len(loaded.DefaultShareWith), len(original.DefaultShareWith))
	}
	if loaded.ReconcileInterval != original.ReconcileInterval {
		t.Errorf("ReconcileInterval = %q, want %q", loaded.ReconcileInterval, original.ReconcileInterval)
	}
	if len(loaded.Discovery.ScanRoots) != len(original.Discovery.ScanRoots) {
		t.Errorf("ScanRoots len = %d, want %d", len(loaded.Discovery.ScanRoots), len(original.Discovery.ScanRoots))
	}
	if len(loaded.Discovery.Exclude) != len(original.Discovery.Exclude) {
		t.Errorf("Exclude len = %d, want %d", len(loaded.Discovery.Exclude), len(original.Discovery.Exclude))
	}
	if loaded.Discovery.ScanInterval != original.Discovery.ScanInterval {
		t.Errorf("ScanInterval = %q, want %q", loaded.Discovery.ScanInterval, original.Discovery.ScanInterval)
	}
	if loaded.Discovery.ScanDepth != original.Discovery.ScanDepth {
		t.Errorf("ScanDepth = %d, want %d", loaded.Discovery.ScanDepth, original.Discovery.ScanDepth)
	}
}

func TestLoadMachineV2Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, err := LoadMachineConfigV2()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil, got %+v", cfg)
	}
}

// --- RepoConfigV2 tests ---

func TestRepoV2RoundTrip(t *testing.T) {
	tmp := t.TempDir()

	original := &RepoConfigV2{
		SchemaVersion: 2,
		Meta: RepoMeta{
			Name:    "my-project",
			Added:   "2026-04-24T10:00:00Z",
			AddedBy: "desktop",
		},
		Sync: RepoSyncConfig{
			SyncthingFolderID: "abc-123",
			Ignore:            []string{"*.log", "node_modules"},
			ShareWith:         []string{"elitebook"},
		},
		Commit: RepoCommitConfig{
			Policy:      "on-idle",
			IdleSeconds: 300,
		},
		GitBackup: RepoGitBackupConfig{
			Interval:  "daily",
			SkipSlots: []uint{2, 3},
		},
	}

	if err := WriteRepoConfigV2(tmp, original); err != nil {
		t.Fatalf("WriteRepoConfigV2: %v", err)
	}

	loaded, err := LoadRepoConfigV2(tmp)
	if err != nil {
		t.Fatalf("LoadRepoConfigV2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadRepoConfigV2 returned nil")
	}

	if loaded.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", loaded.SchemaVersion, original.SchemaVersion)
	}
	if loaded.Meta.Name != original.Meta.Name {
		t.Errorf("Meta.Name = %q, want %q", loaded.Meta.Name, original.Meta.Name)
	}
	if loaded.Meta.Added != original.Meta.Added {
		t.Errorf("Meta.Added = %q, want %q", loaded.Meta.Added, original.Meta.Added)
	}
	if loaded.Meta.AddedBy != original.Meta.AddedBy {
		t.Errorf("Meta.AddedBy = %q, want %q", loaded.Meta.AddedBy, original.Meta.AddedBy)
	}
	if loaded.Sync.SyncthingFolderID != original.Sync.SyncthingFolderID {
		t.Errorf("SyncthingFolderID = %q, want %q", loaded.Sync.SyncthingFolderID, original.Sync.SyncthingFolderID)
	}
	if len(loaded.Sync.Ignore) != 2 {
		t.Errorf("Sync.Ignore len = %d, want 2", len(loaded.Sync.Ignore))
	}
	if len(loaded.Sync.ShareWith) != 1 || loaded.Sync.ShareWith[0] != "elitebook" {
		t.Errorf("Sync.ShareWith = %v, want [elitebook]", loaded.Sync.ShareWith)
	}
	if loaded.Commit.Policy != "on-idle" {
		t.Errorf("Commit.Policy = %q, want %q", loaded.Commit.Policy, "on-idle")
	}
	if loaded.Commit.IdleSeconds != 300 {
		t.Errorf("Commit.IdleSeconds = %d, want 300", loaded.Commit.IdleSeconds)
	}
	if loaded.GitBackup.Interval != "daily" {
		t.Errorf("GitBackup.Interval = %q, want %q", loaded.GitBackup.Interval, "daily")
	}
	if len(loaded.GitBackup.SkipSlots) != 2 {
		t.Errorf("GitBackup.SkipSlots len = %d, want 2", len(loaded.GitBackup.SkipSlots))
	}
}

func TestLoadRepoV2Missing(t *testing.T) {
	tmp := t.TempDir()
	cfg, err := LoadRepoConfigV2(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil, got %+v", cfg)
	}
}

func TestRepoV2EmptySlices(t *testing.T) {
	// Loading a minimal file should yield non-nil slices.
	tmp := t.TempDir()
	minimal := &RepoConfigV2{
		SchemaVersion: 2,
		Meta:          RepoMeta{Name: "minimal"},
	}
	if err := WriteRepoConfigV2(tmp, minimal); err != nil {
		t.Fatalf("WriteRepoConfigV2: %v", err)
	}
	loaded, err := LoadRepoConfigV2(tmp)
	if err != nil {
		t.Fatalf("LoadRepoConfigV2: %v", err)
	}
	if loaded.Sync.Ignore == nil {
		t.Error("Sync.Ignore should not be nil after load")
	}
	if loaded.Sync.ShareWith == nil {
		t.Error("Sync.ShareWith should not be nil after load")
	}
	if loaded.GitBackup.SkipSlots == nil {
		t.Error("GitBackup.SkipSlots should not be nil after load")
	}
}

// --- DetectRepoConfigVersion tests ---

func TestDetectRepoConfigVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name: "v2 file",
			content: `schema_version = 2
[repo]
name = "my-repo"
added = "2026-01-01T00:00:00Z"
added_by = "desktop"
`,
			want: 2,
		},
		{
			name: "v1 legacy file (no schema_version)",
			content: `[repo]
name = "old-repo"
added = "2025-01-01T00:00:00Z"
added_by = "desktop"
`,
			want: 1,
		},
		{
			name:    "missing file",
			content: "", // will not be written
			want:    0,
		},
		{
			name:    "invalid TOML",
			content: "not valid toml }{",
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != "" || tt.name == "invalid TOML" {
				path := filepath.Join(dir, "dotkeeper.toml")
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			got := DetectRepoConfigVersion(dir)
			if got != tt.want {
				t.Errorf("DetectRepoConfigVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- StateV2 tests ---

func TestStateV2RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	now := time.Now().UTC().Truncate(time.Second)

	original := &StateV2{
		SchemaVersion:     2,
		SyncthingDeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH",
		Peers: []PeerEntry{
			{Name: "elitebook", DeviceID: "EEEEEEE-LLLLLLL-1111111-2222222-3333333-4444444-5555555-6666666", LearnedAt: now},
		},
		TrackedOverrides: []string{"/home/user/special-repo"},
		ObservedRepos: map[string]ObservedRepo{
			"/home/user/project": {
				LastReconciledCommit: "abc123",
				LastPushedCommit:     "def456",
				LastBackupAt:         now,
			},
		},
		LastSeenPeers: map[string]time.Time{
			"EEEEEEE-LLLLLLL-1111111-2222222-3333333-4444444-5555555-6666666": now,
		},
	}

	if err := WriteStateV2(original); err != nil {
		t.Fatalf("WriteStateV2: %v", err)
	}

	// Verify file permissions.
	info, err := os.Stat(StateV2Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state.toml permissions = %04o, want 0600", perm)
	}

	loaded, err := LoadStateV2()
	if err != nil {
		t.Fatalf("LoadStateV2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadStateV2 returned nil")
	}

	if loaded.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", loaded.SchemaVersion, original.SchemaVersion)
	}
	if loaded.SyncthingDeviceID != original.SyncthingDeviceID {
		t.Errorf("SyncthingDeviceID = %q, want %q", loaded.SyncthingDeviceID, original.SyncthingDeviceID)
	}
	if len(loaded.Peers) != 1 {
		t.Fatalf("Peers len = %d, want 1", len(loaded.Peers))
	}
	if loaded.Peers[0].Name != "elitebook" {
		t.Errorf("Peer name = %q, want elitebook", loaded.Peers[0].Name)
	}
	if len(loaded.TrackedOverrides) != 1 {
		t.Errorf("TrackedOverrides len = %d, want 1", len(loaded.TrackedOverrides))
	}
	obs, ok := loaded.ObservedRepos["/home/user/project"]
	if !ok {
		t.Fatal("ObservedRepos missing /home/user/project")
	}
	if obs.LastReconciledCommit != "abc123" {
		t.Errorf("LastReconciledCommit = %q, want abc123", obs.LastReconciledCommit)
	}
	if obs.LastPushedCommit != "def456" {
		t.Errorf("LastPushedCommit = %q, want def456", obs.LastPushedCommit)
	}
	if obs.LastBackupAt.IsZero() {
		t.Error("LastBackupAt should not be zero after round-trip")
	}
	if len(loaded.LastSeenPeers) != 1 {
		t.Errorf("LastSeenPeers len = %d, want 1", len(loaded.LastSeenPeers))
	}
}

func TestLoadStateV2Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	s, err := LoadStateV2()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil, got %+v", s)
	}
}

func TestStateV2EmptySlicesAndMaps(t *testing.T) {
	// A minimal state file should yield non-nil collections after load.
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	minimal := &StateV2{SchemaVersion: 2}
	if err := WriteStateV2(minimal); err != nil {
		t.Fatalf("WriteStateV2: %v", err)
	}
	loaded, err := LoadStateV2()
	if err != nil {
		t.Fatalf("LoadStateV2: %v", err)
	}
	if loaded.Peers == nil {
		t.Error("Peers should not be nil")
	}
	if loaded.TrackedOverrides == nil {
		t.Error("TrackedOverrides should not be nil")
	}
	if loaded.ObservedRepos == nil {
		t.Error("ObservedRepos should not be nil")
	}
	if loaded.LastSeenPeers == nil {
		t.Error("LastSeenPeers should not be nil")
	}
}

// --- PathsV2 tests ---

func TestStateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	got := StateDir()
	want := filepath.Join(tmp, "dotkeeper")
	if got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home, _ := os.UserHomeDir()
	got := StateDir()
	want := filepath.Join(home, ".local", "state", "dotkeeper")
	if got != want {
		t.Errorf("StateDir() fallback = %q, want %q", got, want)
	}
}

func TestStateV2Path(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	got := StateV2Path()
	want := filepath.Join(tmp, "dotkeeper", "state.toml")
	if got != want {
		t.Errorf("StateV2Path() = %q, want %q", got, want)
	}
}

func TestRepoConfigPath(t *testing.T) {
	got := RepoConfigPath("/home/user/my-repo")
	want := "/home/user/my-repo/dotkeeper.toml"
	if got != want {
		t.Errorf("RepoConfigPath() = %q, want %q", got, want)
	}
}

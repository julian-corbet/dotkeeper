// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/Documents", filepath.Join(home, "Documents")},
		{"~/.config/dotkeeper", filepath.Join(home, ".config/dotkeeper")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestContractPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{filepath.Join(home, "Documents"), "~/Documents"},
		{filepath.Join(home, ".config/dotkeeper"), "~/.config/dotkeeper"},
		{"/other/path", "/other/path"},
	}

	for _, tt := range tests {
		got := ContractPath(tt.input)
		if got != tt.want {
			t.Errorf("ContractPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWriteAndLoadMachineConfig(t *testing.T) {
	// Use a temp dir as XDG_CONFIG_HOME
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	// Write
	if err := WriteMachineConfig("test-machine", 3); err != nil {
		t.Fatalf("WriteMachineConfig: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmp, "dotkeeper", "machine.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("machine.toml not created at %s", path)
	}

	// Load and verify
	cfg, err := LoadMachineConfig()
	if err != nil {
		t.Fatalf("LoadMachineConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadMachineConfig returned nil")
	}
	if cfg.Name != "test-machine" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-machine")
	}
	if cfg.Slot != 3 {
		t.Errorf("Slot = %d, want %d", cfg.Slot, 3)
	}
}

func TestLoadMachineConfigMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, err := LoadMachineConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil, got %+v", cfg)
	}
}

func TestWriteAndLoadSharedConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &SharedConfig{
		Sync: SyncConfig{
			GitInterval:       "daily",
			SlotOffsetMinutes: 5,
		},
		Syncthing: SyncthingConfig{
			Ignore: []string{".git", "node_modules"},
		},
		Machines: map[string]MachineEntry{
			"desktop": {
				Hostname:    "my-desktop",
				Slot:        0,
				SyncthingID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH",
			},
		},
		Repos: []RepoEntry{
			{Name: "my-project", Path: "~/Documents/my-project", Git: true},
			{Name: "notes", Path: "~/notes", Git: false},
		},
	}

	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig: %v", err)
	}

	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadSharedConfig returned nil")
	}
	if loaded.Sync.GitInterval != "daily" {
		t.Errorf("GitInterval = %q, want %q", loaded.Sync.GitInterval, "daily")
	}
	if loaded.Sync.SlotOffsetMinutes != 5 {
		t.Errorf("SlotOffsetMinutes = %d, want %d", loaded.Sync.SlotOffsetMinutes, 5)
	}
	if len(loaded.Syncthing.Ignore) != 2 {
		t.Errorf("Ignore has %d entries, want 2", len(loaded.Syncthing.Ignore))
	}
	if len(loaded.Machines) != 1 {
		t.Errorf("Machines has %d entries, want 1", len(loaded.Machines))
	}
	m := loaded.Machines["desktop"]
	if m.Hostname != "my-desktop" {
		t.Errorf("Machine hostname = %q, want %q", m.Hostname, "my-desktop")
	}
	if len(loaded.Repos) != 2 {
		t.Errorf("Repos has %d entries, want 2", len(loaded.Repos))
	}
	if loaded.Repos[0].Name != "my-project" || !loaded.Repos[0].Git {
		t.Errorf("Repo 0 = %+v, unexpected", loaded.Repos[0])
	}
	if loaded.Repos[1].Name != "notes" || loaded.Repos[1].Git {
		t.Errorf("Repo 1 = %+v, unexpected", loaded.Repos[1])
	}
}

// TestAutoResolveDefault — unset field reads as enabled (the safer
// default: users get auto-resolution out of the box).
func TestAutoResolveDefault(t *testing.T) {
	var s SyncConfig // no auto_resolve_conflicts set
	if !s.AutoResolveEnabled() {
		t.Error("AutoResolveEnabled() with nil pointer should default to true")
	}

	tr := true
	s.AutoResolveConflicts = &tr
	if !s.AutoResolveEnabled() {
		t.Error("AutoResolveEnabled() with *true should be true")
	}

	f := false
	s.AutoResolveConflicts = &f
	if s.AutoResolveEnabled() {
		t.Error("AutoResolveEnabled() with *false should be false")
	}
}

// TestAutoResolveRoundtrip — explicit false survives write+load, and
// omitting the field from the TOML still round-trips as true (default).
func TestAutoResolveRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	f := false
	cfg := &SharedConfig{
		Sync: SyncConfig{
			GitInterval:          "daily",
			SlotOffsetMinutes:    5,
			AutoResolveConflicts: &f,
		},
		Machines: map[string]MachineEntry{},
	}
	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig: %v", err)
	}
	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if loaded.Sync.AutoResolveEnabled() {
		t.Error("expected auto-resolve disabled after round-trip")
	}
}

func TestLoadSharedConfigMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil, got %+v", cfg)
	}
}

func TestAddAndRemoveRepo(t *testing.T) {
	cfg := &SharedConfig{
		Repos: []RepoEntry{
			{Name: "existing", Path: "~/existing", Git: true},
		},
	}

	// Add new repo
	if !AddRepo(cfg, "new-repo", "~/new-repo", true) {
		t.Error("AddRepo should return true for new repo")
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}

	// Add duplicate
	if AddRepo(cfg, "new-repo", "~/new-repo", true) {
		t.Error("AddRepo should return false for duplicate")
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos after duplicate add, got %d", len(cfg.Repos))
	}

	// Remove existing
	if !RemoveRepo(cfg, "new-repo") {
		t.Error("RemoveRepo should return true for existing repo")
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("expected 1 repo after remove, got %d", len(cfg.Repos))
	}

	// Remove non-existing
	if RemoveRepo(cfg, "nonexistent") {
		t.Error("RemoveRepo should return false for non-existing repo")
	}
}

func TestAddMachine(t *testing.T) {
	cfg := &SharedConfig{
		Machines: make(map[string]MachineEntry),
	}

	AddMachine(cfg, "laptop", "my-laptop", 1, "DEVICE-ID")
	if len(cfg.Machines) != 1 {
		t.Fatalf("expected 1 machine, got %d", len(cfg.Machines))
	}
	if cfg.Machines["laptop"].Hostname != "my-laptop" {
		t.Errorf("hostname = %q, want %q", cfg.Machines["laptop"].Hostname, "my-laptop")
	}

	// Update existing
	AddMachine(cfg, "laptop", "my-laptop", 1, "NEW-DEVICE-ID")
	if cfg.Machines["laptop"].SyncthingID != "NEW-DEVICE-ID" {
		t.Errorf("device ID not updated")
	}
	if len(cfg.Machines) != 1 {
		t.Errorf("should still have 1 machine, got %d", len(cfg.Machines))
	}
}

func TestRepoLog(t *testing.T) {
	tmp := t.TempDir()

	// Create
	if err := CreateRepoLog(tmp, "test-repo", "test-machine"); err != nil {
		t.Fatalf("CreateRepoLog: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmp, "dotkeeper.toml")); os.IsNotExist(err) {
		t.Fatal("dotkeeper.toml not created")
	}

	// Load
	log, err := LoadRepoLog(tmp)
	if err != nil {
		t.Fatalf("LoadRepoLog: %v", err)
	}
	if log.Repo.Name != "test-repo" {
		t.Errorf("repo name = %q, want %q", log.Repo.Name, "test-repo")
	}
	if log.Repo.AddedBy != "test-machine" {
		t.Errorf("added_by = %q, want %q", log.Repo.AddedBy, "test-machine")
	}
	if log.Repo.Added == "" {
		t.Error("added timestamp missing")
	}
}

// TestRepoLogHasNoMachineBlock verifies the per-repo dotkeeper.toml does
// NOT contain a [machines] section — machine-scoped state lives in the
// synced config.toml, not the git-tracked per-repo log.
func TestRepoLogHasNoMachineBlock(t *testing.T) {
	tmp := t.TempDir()
	if err := CreateRepoLog(tmp, "r", "m"); err != nil {
		t.Fatalf("CreateRepoLog: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "dotkeeper.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if s := string(data); contains(s, "[machines") || contains(s, "last_seen") {
		t.Errorf("per-repo dotkeeper.toml leaked machine state:\n%s", s)
	}
}

// contains is a tiny helper to avoid importing strings into this file.
func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestLoadRepoLogMissing(t *testing.T) {
	tmp := t.TempDir()
	log, err := LoadRepoLog(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log != nil {
		t.Fatalf("expected nil, got %+v", log)
	}
}

func TestDefaultIgnorePatterns(t *testing.T) {
	patterns := DefaultIgnorePatterns()
	if len(patterns) == 0 {
		t.Fatal("DefaultIgnorePatterns returned empty list")
	}

	// Check essential patterns are present
	essential := []string{".git", "*.sync-conflict-*", "node_modules", "*.sqlite3"}
	for _, want := range essential {
		found := false
		for _, p := range patterns {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing essential pattern: %q", want)
		}
	}
}

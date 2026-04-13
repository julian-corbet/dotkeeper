// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTOMLRoundTrip verifies that WriteSharedConfig output can be read back.
func TestTOMLRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git", "node_modules"}},
		Machines: map[string]MachineEntry{
			"my_desktop": {Hostname: "my-desktop", Slot: 0, SyncthingID: "AAA-BBB"},
			"my_laptop":  {Hostname: "my-laptop", Slot: 1, SyncthingID: "CCC-DDD"},
		},
		Repos: []RepoEntry{
			{Name: "project", Path: "~/project", Git: true},
		},
	}

	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig: %v", err)
	}

	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}

	// Verify machines round-trip
	if len(loaded.Machines) != 2 {
		t.Fatalf("expected 2 machines, got %d", len(loaded.Machines))
	}
	for _, key := range []string{"my_desktop", "my_laptop"} {
		if _, ok := loaded.Machines[key]; !ok {
			t.Errorf("machine key %q missing after round-trip", key)
		}
	}
}

// TestTOMLRoundTripSpecialChars tests machine names with dots, spaces, etc.
func TestTOMLRoundTripSpecialChars(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git"}},
		Machines: map[string]MachineEntry{
			"host_with_dots":   {Hostname: "my.host.local", Slot: 0, SyncthingID: "AAA"},
			"host_with_spaces": {Hostname: "My Machine", Slot: 1, SyncthingID: "BBB"},
		},
	}

	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig: %v", err)
	}

	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}

	if len(loaded.Machines) != 2 {
		// Read the file to see what was written
		data, _ := os.ReadFile(SharedConfigPath())
		t.Fatalf("expected 2 machines, got %d.\nFile contents:\n%s", len(loaded.Machines), data)
	}
}

// TestRepoLogRoundTrip verifies per-repo dotkeeper.toml round-trips.
func TestRepoLogRoundTrip(t *testing.T) {
	tmp := t.TempDir()

	if err := CreateRepoLog(tmp, "test-repo", "machine_a"); err != nil {
		t.Fatalf("CreateRepoLog: %v", err)
	}

	// Touch from another machine
	if err := TouchRepoLog(tmp, "machine_b"); err != nil {
		t.Fatalf("TouchRepoLog: %v", err)
	}

	log, err := LoadRepoLog(tmp)
	if err != nil {
		t.Fatalf("LoadRepoLog: %v", err)
	}

	if len(log.Machines) != 2 {
		data, _ := os.ReadFile(filepath.Join(tmp, "dotkeeper.toml"))
		t.Fatalf("expected 2 machines, got %d.\nFile:\n%s", len(log.Machines), data)
	}

	if _, ok := log.Machines["machine_a"]; !ok {
		t.Error("machine_a missing")
	}
	if _, ok := log.Machines["machine_b"]; !ok {
		t.Error("machine_b missing")
	}
}

// TestRepoLogNoConflictMarkers verifies dotkeeper.toml never contains git conflict markers.
func TestRepoLogNoConflictMarkers(t *testing.T) {
	tmp := t.TempDir()

	CreateRepoLog(tmp, "test", "machine_a")
	TouchRepoLog(tmp, "machine_b")

	data, _ := os.ReadFile(filepath.Join(tmp, "dotkeeper.toml"))
	content := string(data)

	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>"} {
		if strings.Contains(content, marker) {
			t.Errorf("dotkeeper.toml contains conflict marker %q:\n%s", marker, content)
		}
	}
}

// TestFilePermissions verifies config files are created with restricted permissions.
func TestFilePermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	WriteMachineConfig("test", 0)
	info, err := os.Stat(MachineConfigPath())
	if err != nil {
		t.Fatalf("stat machine.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("machine.toml perm = %o, want 0600", perm)
	}

	WriteSharedConfig(&SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git"}},
		Machines:  make(map[string]MachineEntry),
	})
	info, err = os.Stat(SharedConfigPath())
	if err != nil {
		t.Fatalf("stat config.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.toml perm = %o, want 0600", perm)
	}

	// Config dir should be 0700
	info, err = os.Stat(ConfigDir())
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir perm = %o, want 0700", perm)
	}
}

// TestContractPathCrossPlatform tests ContractPath with explicit separators.
func TestContractPathCrossPlatform(t *testing.T) {
	home, _ := os.UserHomeDir()

	// Should always work regardless of platform
	got := ContractPath(filepath.Join(home, "Documents", "project"))
	if !strings.HasPrefix(got, "~/") {
		t.Errorf("ContractPath didn't contract: %q", got)
	}
	// Result should use forward slashes
	if strings.Contains(got, "\\") {
		t.Errorf("ContractPath produced backslashes: %q", got)
	}
}

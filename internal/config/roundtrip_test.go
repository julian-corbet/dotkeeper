// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"strings"
	"testing"
)

// TestMachineConfigV2RoundTrip verifies that WriteMachineConfigV2 output
// can be read back cleanly by LoadMachineConfigV2.
func TestMachineConfigV2RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &MachineConfigV2{
		SchemaVersion:       2,
		Name:                "test-machine",
		Slot:                1,
		DefaultCommitPolicy: "manual",
		DefaultGitInterval:  "hourly",
		ReconcileInterval:   "5m",
		Discovery: DiscoveryConfig{
			ScanRoots:    []string{"~/Documents", "~/.agent"},
			ScanDepth:    3,
			ScanInterval: "5m",
		},
	}

	if err := WriteMachineConfigV2(cfg); err != nil {
		t.Fatalf("WriteMachineConfigV2: %v", err)
	}

	loaded, err := LoadMachineConfigV2()
	if err != nil {
		t.Fatalf("LoadMachineConfigV2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMachineConfigV2 returned nil")
	}
	if loaded.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, cfg.Name)
	}
	if loaded.Slot != cfg.Slot {
		t.Errorf("Slot = %d, want %d", loaded.Slot, cfg.Slot)
	}
	if len(loaded.Discovery.ScanRoots) != 2 {
		t.Errorf("ScanRoots = %v, want 2 entries", loaded.Discovery.ScanRoots)
	}
}

// TestFilePermissions verifies config files are created with restricted permissions.
func TestFilePermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	mcfg := &MachineConfigV2{
		SchemaVersion: 2,
		Name:          "test",
		Slot:          0,
		Discovery:     DiscoveryConfig{},
	}
	if err := WriteMachineConfigV2(mcfg); err != nil {
		t.Fatalf("WriteMachineConfigV2: %v", err)
	}
	info, err := os.Stat(MachineConfigPath())
	if err != nil {
		t.Fatalf("stat machine.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("machine.toml perm = %o, want 0600", perm)
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
	got := ContractPath(home + "/Documents/project")
	if !strings.HasPrefix(got, "~/") {
		t.Errorf("ContractPath didn't contract: %q", got)
	}
	// Result should use forward slashes
	if strings.Contains(got, "\\") {
		t.Errorf("ContractPath produced backslashes: %q", got)
	}
}

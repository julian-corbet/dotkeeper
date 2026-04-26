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

// TestWriteAndLoadMachineConfig verifies that WriteMachineConfigV2 and
// LoadMachineConfigV2 round-trip the v0.5 machine identity correctly.
func TestWriteAndLoadMachineConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	mcfg := &MachineConfigV2{
		SchemaVersion: 2,
		Name:          "test-machine",
		Slot:          3,
		Discovery: DiscoveryConfig{
			ScanRoots: []string{"~/Documents"},
			ScanDepth: 3,
		},
	}
	if err := WriteMachineConfigV2(mcfg); err != nil {
		t.Fatalf("WriteMachineConfigV2: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmp, "dotkeeper", "machine.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("machine.toml not created at %s", path)
	}

	// Load and verify
	loaded, err := LoadMachineConfigV2()
	if err != nil {
		t.Fatalf("LoadMachineConfigV2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMachineConfigV2 returned nil")
	}
	if loaded.Name != "test-machine" {
		t.Errorf("Name = %q, want %q", loaded.Name, "test-machine")
	}
	if loaded.Slot != 3 {
		t.Errorf("Slot = %d, want %d", loaded.Slot, 3)
	}
}

// TestLoadMachineConfigMissing verifies that LoadMachineConfigV2 returns
// nil (not an error) when machine.toml does not exist yet.
func TestLoadMachineConfigMissing(t *testing.T) {
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

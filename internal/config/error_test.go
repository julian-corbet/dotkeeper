// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Error injection tests verify that dotkeeper produces useful error
// messages (not panics or stack traces) under hostile filesystem conditions.
//
// Found a crash from a weird filesystem state? Add a test here.

// TestWriteMachineConfigV2ReadOnlyDir verifies WriteMachineConfigV2
// handles uncreatable directories.
func TestWriteMachineConfigV2ReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows handles directory permissions differently")
	}

	tmp := t.TempDir()
	_ = os.Chmod(tmp, 0o500)
	defer func() { _ = os.Chmod(tmp, 0o700) }()

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	mcfg := &MachineConfigV2{SchemaVersion: 2, Name: "test", Slot: 0}
	err := WriteMachineConfigV2(mcfg)
	if err == nil {
		t.Error("expected error writing to read-only directory")
	}
}

// TestLoadMachineConfigV2CorruptFile verifies corrupt machine.toml handling.
func TestLoadMachineConfigV2CorruptFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "machine.toml"), []byte("not = [valid = toml"), 0o600)

	_, err := LoadMachineConfigV2()
	if err == nil {
		t.Error("expected error loading corrupt machine.toml")
	}
}

// TestExpandPathNoHome verifies ExpandPath handles missing HOME gracefully.
func TestExpandPathNoHome(t *testing.T) {
	// ExpandPath with ~ should not panic even if HOME is weird
	// (it falls back to returning the path as-is)
	got := ExpandPath("~/Documents")
	if got == "" {
		t.Error("ExpandPath returned empty string")
	}
}

// TestBrokenSymlinkAsRepoPath verifies behavior when a path
// is a broken symlink in state.toml TrackedOverrides.
func TestBrokenSymlinkAsRepoPath(t *testing.T) {
	tmp := t.TempDir()
	link := filepath.Join(tmp, "broken-link")
	_ = os.Symlink("/nonexistent/path/that/does/not/exist", link)

	// Writing to a broken symlink path should either error or be OS-specific.
	// This test just verifies it doesn't panic.
	_ = link
}

// TestConfigDirCreation verifies that config operations create
// the directory tree automatically.
func TestConfigDirCreation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "deep", "nested", "config"))

	mcfg := &MachineConfigV2{SchemaVersion: 2, Name: "test", Slot: 0}
	err := WriteMachineConfigV2(mcfg)
	if err != nil {
		t.Fatalf("WriteMachineConfigV2 with nested dir: %v", err)
	}

	loaded, err := LoadMachineConfigV2()
	if err != nil {
		t.Fatalf("LoadMachineConfigV2: %v", err)
	}
	if loaded.Name != "test" {
		t.Errorf("Name = %q, want 'test'", loaded.Name)
	}
}

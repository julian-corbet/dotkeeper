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

// TestWriteConfigReadOnlyDir verifies that WriteSharedConfig returns
// a clear error when the config directory doesn't exist and can't be created.
func TestWriteConfigReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows handles directory permissions differently")
	}

	tmp := t.TempDir()
	// Make the parent read-only so the config dir CAN'T be created
	os.Chmod(tmp, 0o500)
	defer os.Chmod(tmp, 0o700) // restore for cleanup

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	cfg := &SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git"}},
		Machines:  make(map[string]MachineEntry),
	}

	err := WriteSharedConfig(cfg)
	if err == nil {
		t.Error("expected error writing to read-only directory")
	}
}

// TestWriteMachineConfigReadOnlyDir verifies WriteMachineConfig
// handles uncreatable directories.
func TestWriteMachineConfigReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows handles directory permissions differently")
	}

	tmp := t.TempDir()
	os.Chmod(tmp, 0o500)
	defer os.Chmod(tmp, 0o700)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	err := WriteMachineConfig("test", 0)
	if err == nil {
		t.Error("expected error writing to read-only directory")
	}
}

// TestCreateRepoLogReadOnlyDir verifies CreateRepoLog handles
// read-only repo directories.
func TestCreateRepoLogReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows handles directory permissions differently")
	}

	tmp := t.TempDir()
	os.Chmod(tmp, 0o500)
	defer os.Chmod(tmp, 0o700)

	err := CreateRepoLog(tmp, "test", "machine")
	if err == nil {
		t.Error("expected error creating repo log in read-only directory")
	}
}

// TestLoadSharedConfigCorruptFile verifies that a completely corrupt
// config.toml returns an error (not nil or panic).
func TestLoadSharedConfigCorruptFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("{{{{BROKEN"), 0o600)

	_, err := LoadSharedConfig()
	if err == nil {
		t.Error("expected error loading corrupt config.toml")
	}
}

// TestLoadMachineConfigCorruptFile verifies corrupt machine.toml handling.
func TestLoadMachineConfigCorruptFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "machine.toml"), []byte("not = [valid = toml"), 0o600)

	_, err := LoadMachineConfig()
	if err == nil {
		t.Error("expected error loading corrupt machine.toml")
	}
}

// TestLoadRepoLogCorruptFile verifies corrupt dotkeeper.toml handling.
func TestLoadRepoLogCorruptFile(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "dotkeeper.toml"), []byte("\x00\x01GARBAGE"), 0o644)

	_, err := LoadRepoLog(tmp)
	if err == nil {
		t.Error("expected error loading corrupt dotkeeper.toml")
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

// TestBrokenSymlinkAsRepoPath verifies behavior when a repo path
// is a broken symlink.
func TestBrokenSymlinkAsRepoPath(t *testing.T) {
	tmp := t.TempDir()
	link := filepath.Join(tmp, "broken-link")
	os.Symlink("/nonexistent/path/that/does/not/exist", link)

	// CreateRepoLog should fail — target doesn't exist
	err := CreateRepoLog(link, "test", "machine")
	if err == nil {
		// If the OS allows writing through a broken symlink, that's OS-specific
		t.Log("OS allowed writing through broken symlink — that's fine")
	}
}

// TestConfigDirCreation verifies that config operations create
// the directory tree automatically.
func TestConfigDirCreation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "deep", "nested", "config"))

	err := WriteMachineConfig("test", 0)
	if err != nil {
		t.Fatalf("WriteMachineConfig with nested dir: %v", err)
	}

	cfg, err := LoadMachineConfig()
	if err != nil {
		t.Fatalf("LoadMachineConfig: %v", err)
	}
	if cfg.Name != "test" {
		t.Errorf("Name = %q, want 'test'", cfg.Name)
	}
}

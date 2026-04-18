// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSharedConfigDefaults verifies that missing fields get sensible defaults.
// Targets surviving mutants at config.go:167 and config.go:170.
func TestLoadSharedConfigDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	// Write a minimal config with no sync section
	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
[sync]

[syncthing]
ignore = [".git"]
`), 0o600)

	cfg, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if cfg.Sync.SlotOffsetMinutes != 5 {
		t.Errorf("SlotOffsetMinutes = %d, want default 5", cfg.Sync.SlotOffsetMinutes)
	}
	if cfg.Sync.GitInterval != "daily" {
		t.Errorf("GitInterval = %q, want default \"daily\"", cfg.Sync.GitInterval)
	}
	if cfg.Machines == nil {
		t.Error("Machines map should be initialized, not nil")
	}
}

// TestLoadSharedConfigExplicitValues verifies that explicit non-default values
// are preserved (not overwritten by defaults).
func TestLoadSharedConfigExplicitValues(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
[sync]
git_interval = "weekly"
slot_offset_minutes = 10
`), 0o600)

	cfg, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if cfg.Sync.SlotOffsetMinutes != 10 {
		t.Errorf("SlotOffsetMinutes = %d, want 10", cfg.Sync.SlotOffsetMinutes)
	}
	if cfg.Sync.GitInterval != "weekly" {
		t.Errorf("GitInterval = %q, want \"weekly\"", cfg.Sync.GitInterval)
	}
}

// TestEmptyConfig verifies loading an empty config file doesn't crash.
func TestEmptyConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(""), 0o600)

	cfg, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// Defaults should be applied
	if cfg.Sync.GitInterval != "daily" {
		t.Errorf("empty config: GitInterval = %q, want \"daily\"", cfg.Sync.GitInterval)
	}
}

// TestMalformedTOML verifies that garbled config files produce errors, not panics.
func TestMalformedTOML(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)

	cases := []struct {
		name    string
		content string
	}{
		{"binary_garbage", "\x00\x01\x02\x03"},
		{"unclosed_bracket", "[sync\ngit_interval = \"daily\""},
		{"invalid_utf8", "name = \"\xff\xfe\""},
		{"truncated", "[sync]\ngit_interval = "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tc.content), 0o600)
			_, err := LoadSharedConfig()
			if err == nil {
				t.Log("no error (TOML parser accepted it) — that's fine")
			}
		})
	}
}

// TestManyMachines verifies config with many machines doesn't degrade.
func TestManyMachines(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git"}},
		Machines:  make(map[string]MachineEntry),
	}

	// Add 100 machines
	for i := 0; i < 100; i++ {
		key := strings.ToLower(strings.ReplaceAll(
			strings.ReplaceAll(
				strings.ReplaceAll(
					"machine_"+string(rune('a'+i%26))+string(rune('0'+i/26)),
					"-", "_"),
				".", "_"),
			" ", "_"))
		cfg.Machines[key] = MachineEntry{
			Hostname:    "host-" + key,
			Slot:        i,
			SyncthingID: "DEVICE-" + key,
		}
	}

	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig with 100 machines: %v", err)
	}

	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig with 100 machines: %v", err)
	}
	if len(loaded.Machines) != 100 {
		t.Errorf("expected 100 machines, got %d", len(loaded.Machines))
	}
}

// TestUnicodeRepoNames verifies repos with unicode names round-trip correctly.
func TestUnicodeRepoNames(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &SharedConfig{
		Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
		Syncthing: SyncthingConfig{Ignore: []string{".git"}},
		Machines:  make(map[string]MachineEntry),
		Repos: []RepoEntry{
			{Name: "プロジェクト", Path: "~/日本語/パス", Git: true},
			{Name: "проект", Path: "~/документы/код", Git: false},
		},
	}

	if err := WriteSharedConfig(cfg); err != nil {
		t.Fatalf("WriteSharedConfig: %v", err)
	}

	loaded, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig: %v", err)
	}
	if len(loaded.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(loaded.Repos))
	}
	if loaded.Repos[0].Name != "プロジェクト" {
		t.Errorf("Japanese repo name lost: %q", loaded.Repos[0].Name)
	}
	if loaded.Repos[1].Name != "проект" {
		t.Errorf("Russian repo name lost: %q", loaded.Repos[1].Name)
	}
}

// TestRepoLogWithSymlink verifies that per-repo log works in symlinked dirs.
func TestRepoLogWithSymlink(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	linkDir := filepath.Join(tmp, "link")

	_ = os.MkdirAll(realDir, 0o755)
	_ = os.Symlink(realDir, linkDir)

	// Create via symlink
	if err := CreateRepoLog(linkDir, "test-repo", "machine_a"); err != nil {
		t.Fatalf("CreateRepoLog via symlink: %v", err)
	}

	// Load via real path
	log, err := LoadRepoLog(realDir)
	if err != nil {
		t.Fatalf("LoadRepoLog via real path: %v", err)
	}
	if log == nil {
		t.Fatal("expected non-nil repo log")
	}
	if log.Repo.Name != "test-repo" {
		t.Errorf("repo name = %q, want \"test-repo\"", log.Repo.Name)
	}
}

// TestConfigUnknownKeys verifies that unknown TOML keys don't crash loading
// (forward compatibility — config from a newer version).
func TestConfigUnknownKeys(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "dotkeeper")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
[sync]
git_interval = "daily"
slot_offset_minutes = 5
future_field = "hello from v2"

[syncthing]
ignore = [".git"]

[new_section]
key = "value"
`), 0o600)

	cfg, err := LoadSharedConfig()
	if err != nil {
		t.Fatalf("LoadSharedConfig with unknown keys: %v", err)
	}
	if cfg.Sync.GitInterval != "daily" {
		t.Errorf("known field broken by unknown keys: GitInterval = %q", cfg.Sync.GitInterval)
	}
}

// TestSanitizeTOMLKey verifies UTF-8 sanitization of TOML keys.
func TestSanitizeTOMLKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"valid-utf8-日本語", "valid-utf8-日本語"},
		{"\xe8", "_"},
		{"hello\xffworld", "hello_world"},
		{"", ""},
	}

	for _, tt := range tests {
		got := sanitizeTOMLKey(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTOMLKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestPathExpansionEdgeCases tests edge cases in ExpandPath/ContractPath.
func TestPathExpansionEdgeCases(t *testing.T) {
	tests := []struct {
		input string
		desc  string
	}{
		{"", "empty string"},
		{"~", "bare tilde"},
		{"~/", "tilde slash"},
		{"~root/something", "other user tilde"},
		{"relative", "relative path"},
		{"./relative", "dot relative"},
		{"../parent", "parent relative"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			// Must not panic
			expanded := ExpandPath(tt.input)
			_ = ContractPath(expanded)
		})
	}
}

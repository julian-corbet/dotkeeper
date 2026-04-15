// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"testing"
)

// FuzzTOMLRoundTrip tests that WriteSharedConfig → LoadSharedConfig never
// panics for arbitrary machine names and repo paths.
func FuzzTOMLRoundTrip(f *testing.F) {
	f.Add("simple", "my-desktop", "~/Documents/project")
	f.Add("dots.in.name", "host.local", "~/path with spaces/repo")
	f.Add("", "", "")
	f.Add("key\"with\"quotes", "host\nname", "~/\x00path")
	f.Add("[brackets]", "host=value", "~/repo\ttab")
	f.Add("a.b.c.d.e", "日本語", "~/中文/路径")

	f.Fuzz(func(t *testing.T, machineKey, hostname, repoPath string) {
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)

		cfg := &SharedConfig{
			Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
			Syncthing: SyncthingConfig{Ignore: []string{".git"}},
			Machines: map[string]MachineEntry{
				machineKey: {Hostname: hostname, Slot: 0, SyncthingID: "AAA-BBB"},
			},
			Repos: []RepoEntry{
				{Name: "test", Path: repoPath, Git: true},
			},
		}

		// Write must not panic
		err := WriteSharedConfig(cfg)
		if err != nil {
			return // write failure is acceptable for weird inputs
		}

		// Load must not panic
		loaded, err := LoadSharedConfig()
		if err != nil {
			return // parse failure is acceptable for weird inputs
		}
		if loaded == nil {
			t.Error("LoadSharedConfig returned nil after successful write")
		}
	})
}

// FuzzRepoLogRoundTrip tests that CreateRepoLog → TouchRepoLog → LoadRepoLog
// never panics for arbitrary machine names.
func FuzzRepoLogRoundTrip(f *testing.F) {
	f.Add("test-repo", "machine_a", "machine_b")
	f.Add("", "", "")
	f.Add("repo\"name", "key[0]", "host.name")
	f.Add("日本語", "中文", "العربية")

	f.Fuzz(func(t *testing.T, repoName, machine1, machine2 string) {
		tmp := t.TempDir()

		// Must not panic
		if err := CreateRepoLog(tmp, repoName, machine1); err != nil {
			return
		}

		// Touch must not panic
		_ = TouchRepoLog(tmp, machine2)

		// Load must not panic
		log, err := LoadRepoLog(tmp)
		if err != nil {
			return
		}
		if log == nil {
			t.Error("LoadRepoLog returned nil after successful create")
		}
	})
}

// FuzzExpandContractPath tests that ExpandPath and ContractPath never panic.
func FuzzExpandContractPath(f *testing.F) {
	f.Add("~/Documents")
	f.Add("/absolute/path")
	f.Add("relative/path")
	f.Add("")
	f.Add("~")
	f.Add("~/")
	f.Add("~root/something")
	f.Add("/home/user/../../../etc/passwd")

	f.Fuzz(func(t *testing.T, path string) {
		// Must not panic
		expanded := ExpandPath(path)
		_ = ContractPath(expanded)
	})
}

// FuzzMachineKeyQuoting tests that machine keys with special characters
// survive TOML round-trip through WriteSharedConfig/LoadSharedConfig.
func FuzzMachineKeyQuoting(f *testing.F) {
	f.Add("simple_key")
	f.Add("key-with-dashes")
	f.Add("key.with.dots")
	f.Add("key with spaces")
	f.Add("")

	f.Fuzz(func(t *testing.T, key string) {
		if key == "" {
			return // empty keys are invalid
		}

		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)

		cfg := &SharedConfig{
			Sync:      SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
			Syncthing: SyncthingConfig{Ignore: []string{".git"}},
			Machines: map[string]MachineEntry{
				key: {Hostname: "test", Slot: 0, SyncthingID: "AAA"},
			},
		}

		if err := WriteSharedConfig(cfg); err != nil {
			return
		}

		loaded, err := LoadSharedConfig()
		if err != nil {
			return
		}
		if loaded == nil {
			return
		}

		// The key is sanitized before writing (invalid UTF-8 bytes → _),
		// so the round-tripped key is the sanitized version.
		sanitized := sanitizeTOMLKey(key)
		if _, ok := loaded.Machines[sanitized]; !ok {
			t.Errorf("machine key %q (sanitized: %q) lost after round-trip", key, sanitized)
		}
	})
}


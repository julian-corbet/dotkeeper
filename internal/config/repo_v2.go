// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// RepoConfigV2 is the v0.5 schema for the per-repo dotkeeper.toml file that
// lives at the root of each managed repository. This file is tracked in git
// and its presence is the opt-in signal for dotkeeper management (ADR 0001).
type RepoConfigV2 struct {
	SchemaVersion int `toml:"schema_version"`

	// Meta holds repo identity. The TOML key [repo] is kept for backward
	// compatibility with the v0.4 [repo] section.
	Meta RepoMeta `toml:"repo"`

	// Sync holds Syncthing-level configuration for this repo.
	Sync RepoSyncConfig `toml:"sync"`

	// Commit holds per-repo commit policy overrides.
	Commit RepoCommitConfig `toml:"commit"`

	// GitBackup holds per-repo git backup schedule overrides.
	GitBackup RepoGitBackupConfig `toml:"git_backup"`
}

// RepoMeta holds repo identity metadata, kept backward-compatible with the
// v0.4 [repo] block.
type RepoMeta struct {
	Name    string `toml:"name"`
	Added   string `toml:"added"`
	AddedBy string `toml:"added_by"`
}

// RepoSyncConfig holds Syncthing folder configuration for this repo.
type RepoSyncConfig struct {
	// SyncthingFolderID is the Syncthing folder ID for this repo.
	// Empty means not yet registered with Syncthing.
	SyncthingFolderID string `toml:"syncthing_folder_id"`
	// Ignore contains additional per-repo Syncthing ignore patterns.
	Ignore []string `toml:"ignore"`
	// ShareWith lists the peer device names that should receive this repo.
	// Empty means inherit from machine.toml's default_share_with.
	ShareWith []string `toml:"share_with"`
}

// RepoCommitConfig holds per-repo commit policy overrides.
type RepoCommitConfig struct {
	// Policy overrides machine.toml's default_commit_policy for this repo.
	// Allowed values: "manual", "on-idle", "timer". Empty means inherit.
	Policy string `toml:"policy"`
	// IdleSeconds is the inactivity threshold for on-idle commits.
	// Zero means use the machine-level default.
	IdleSeconds uint `toml:"idle_seconds"`
}

// RepoGitBackupConfig holds per-repo git backup schedule overrides.
type RepoGitBackupConfig struct {
	// Interval overrides machine.toml's default_git_interval for this repo.
	// Empty means inherit.
	Interval string `toml:"interval"`
	// SkipSlots lists slot numbers that should NOT run a git backup for this
	// repo (e.g. to avoid redundant backups across machines).
	SkipSlots []uint `toml:"skip_slots"`
}

// LoadRepoConfigV2 reads dotkeeper.toml from repoRoot. Returns nil (no error)
// if the file does not exist.
func LoadRepoConfigV2(repoRoot string) (*RepoConfigV2, error) {
	path := RepoConfigPath(repoRoot)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var cfg RepoConfigV2
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	// Ensure slice fields are non-nil for callers.
	if cfg.Sync.Ignore == nil {
		cfg.Sync.Ignore = []string{}
	}
	if cfg.Sync.ShareWith == nil {
		cfg.Sync.ShareWith = []string{}
	}
	if cfg.GitBackup.SkipSlots == nil {
		cfg.GitBackup.SkipSlots = []uint{}
	}
	return &cfg, nil
}

// WriteRepoConfigV2 writes cfg to dotkeeper.toml in repoRoot. The file is
// written with mode 0644 (it is tracked in git and intended to be readable).
func WriteRepoConfigV2(repoRoot string, cfg *RepoConfigV2) error {
	var b strings.Builder
	b.WriteString("# Managed by dotkeeper — https://github.com/julian-corbet/dotkeeper\n")
	b.WriteString("# This file is tracked in git. Its presence opts this repo into dotkeeper management.\n\n")

	fmt.Fprintf(&b, "schema_version = %d\n\n", cfg.SchemaVersion)

	b.WriteString("[repo]\n")
	fmt.Fprintf(&b, "name = %q\n", cfg.Meta.Name)
	fmt.Fprintf(&b, "added = %q\n", cfg.Meta.Added)
	fmt.Fprintf(&b, "added_by = %q\n", cfg.Meta.AddedBy)

	b.WriteString("\n[sync]\n")
	fmt.Fprintf(&b, "syncthing_folder_id = %q\n", cfg.Sync.SyncthingFolderID)
	b.WriteString("ignore = [\n")
	for _, p := range cfg.Sync.Ignore {
		fmt.Fprintf(&b, "    %q,\n", p)
	}
	b.WriteString("]\n")
	b.WriteString("share_with = [\n")
	for _, s := range cfg.Sync.ShareWith {
		fmt.Fprintf(&b, "    %q,\n", s)
	}
	b.WriteString("]\n")

	b.WriteString("\n[commit]\n")
	fmt.Fprintf(&b, "policy = %q\n", cfg.Commit.Policy)
	fmt.Fprintf(&b, "idle_seconds = %d\n", cfg.Commit.IdleSeconds)

	b.WriteString("\n[git_backup]\n")
	fmt.Fprintf(&b, "interval = %q\n", cfg.GitBackup.Interval)
	b.WriteString("skip_slots = [")
	for i, s := range cfg.GitBackup.SkipSlots {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d", s)
	}
	b.WriteString("]\n")

	path := filepath.Join(repoRoot, "dotkeeper.toml")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// DetectRepoConfigVersion inspects dotkeeper.toml in repoRoot and returns the
// schema version it appears to be:
//
//   - 2 if schema_version = 2 is present
//   - 1 if the file exists but has no schema_version field (legacy v0.4 format)
//   - 0 if the file does not exist or cannot be decoded
func DetectRepoConfigVersion(repoRoot string) int {
	path := RepoConfigPath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	// Use a minimal struct to extract schema_version without full decode.
	var probe struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return 0
	}
	if probe.SchemaVersion >= 2 {
		return probe.SchemaVersion
	}

	// File exists and decoded, but no schema_version — treat as v1.
	return 1
}

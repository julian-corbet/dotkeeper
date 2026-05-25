// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// RepoConfigV2 is the v0.6 schema for the per-repo .dotkeeper.toml file that
// lives at the root of each local managed repository copy. This file is local
// machine state and must not be committed or synced.
type RepoConfigV2 struct {
	SchemaVersion int `toml:"schema_version"`

	// Meta holds repo identity.
	Meta RepoMeta `toml:"repo"`

	// Sync holds Syncthing-level configuration for this repo.
	Sync RepoSyncConfig `toml:"sync"`

	// Commit holds per-repo commit policy overrides.
	Commit RepoCommitConfig `toml:"commit"`

	// GitBackup holds per-repo git backup schedule overrides.
	GitBackup RepoGitBackupConfig `toml:"git_backup"`

	// Git holds the canonical git-remote identity for this repo.
	// Populated by `dotkeeper track` from the local working tree's
	// `origin` remote, or left empty for non-git folders. When
	// non-empty, the Canonical field is the load-bearing identity
	// dotkeeper uses for Syncthing folder labels, subscription
	// matching, and operator-facing surfaces — every machine with
	// the same upstream repo arrives at the same canonical regardless
	// of which URL syntax their git client recorded.
	Git RepoGitConfig `toml:"git"`
}

// RepoGitConfig captures the git-remote identity that lets every
// machine refer to the same upstream repo by the same string.
// Zero-value fields are legitimate for non-git folders (dotfiles
// dirs, scratch areas) — callers should branch on Canonical=="".
type RepoGitConfig struct {
	// Remote is the raw remote URL as recorded in the local
	// repo's `.git/config` (typically `origin`). Stored verbatim
	// for diagnostic purposes; identity comparisons go through
	// Canonical.
	Remote string `toml:"remote"`

	// Canonical is the normalised identity form of Remote, produced
	// by internal/gitident.Canonical. Example:
	// "github.com/julian-corbet/dotkeeper" regardless of whether the
	// underlying Remote was the HTTPS, SCP, or ssh:// variant.
	Canonical string `toml:"canonical"`
}

// RepoMeta holds repo identity metadata.
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

// LoadRepoConfigV2 reads .dotkeeper.toml from repoRoot. Returns nil (no error)
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

// WriteRepoConfigV2 writes cfg to .dotkeeper.toml in repoRoot. The file is
// written with mode 0644 and excluded from Git/Syncthing by dotkeeper.
func WriteRepoConfigV2(repoRoot string, cfg *RepoConfigV2) error {
	var b strings.Builder
	b.WriteString("# Managed by dotkeeper - https://github.com/julian-corbet/dotkeeper\n")
	b.WriteString("# Local machine state. Do not commit or sync this file.\n\n")

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

	// [git] is omitted entirely for non-git folders so the file
	// stays clean for the dotfiles/scratch case. When present,
	// Canonical is the load-bearing identity (subscription matching,
	// folder labels); Remote is kept for diagnostics.
	if cfg.Git.Remote != "" || cfg.Git.Canonical != "" {
		b.WriteString("\n[git]\n")
		fmt.Fprintf(&b, "remote = %q\n", cfg.Git.Remote)
		fmt.Fprintf(&b, "canonical = %q\n", cfg.Git.Canonical)
	}

	path := RepoConfigPath(repoRoot)
	return WriteFileAtomic(path, []byte(b.String()), 0o644)
}

// DetectRepoConfigVersion inspects .dotkeeper.toml in repoRoot and returns the
// schema version it appears to be:
//
//   - 2 if schema_version = 2 is present
//   - 0 if the file does not exist, has no schema_version, or cannot be decoded
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
	return probe.SchemaVersion
}

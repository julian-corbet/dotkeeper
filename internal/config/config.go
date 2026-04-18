// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package config handles dotkeeper configuration loading and path management.
//
// Three config files:
//   - machine.toml  — local identity (name, slot). Never synced.
//   - config.toml   — shared settings (machines, repos, ignore patterns). Synced via Syncthing.
//   - dotkeeper.toml — per-repo log (in each managed repo). Tracked in git.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// --- Shared config (config.toml) ---

type SharedConfig struct {
	Sync      SyncConfig              `toml:"sync"`
	Syncthing SyncthingConfig         `toml:"syncthing"`
	Machines  map[string]MachineEntry `toml:"machines"`
	Repos     []RepoEntry             `toml:"repos"`
}

type SyncConfig struct {
	// GitInterval controls how often the git backup timer fires.
	// Presets: "hourly", "daily", "weekly", "monthly"
	// Custom: "2h", "6h", "12h" (every N hours)
	// Or any systemd OnCalendar expression.
	GitInterval       string `toml:"git_interval"`
	SlotOffsetMinutes int    `toml:"slot_offset_minutes"`
}

type SyncthingConfig struct {
	Ignore []string `toml:"ignore"`
}

type MachineEntry struct {
	Hostname    string `toml:"hostname"`
	Slot        int    `toml:"slot"`
	SyncthingID string `toml:"syncthing_id"`
}

type RepoEntry struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
	Git  bool   `toml:"git"`
}

// --- Machine config (machine.toml) ---

type MachineConfig struct {
	Name string `toml:"name"`
	Slot int    `toml:"slot"`
}

// --- Per-repo log (dotkeeper.toml in each repo) ---

// RepoLog holds the per-repo metadata that lives in the repo root as
// dotkeeper.toml and is tracked in git. It is intentionally scope-limited to
// repo-level identity — machine tracking lives in the shared config.toml.
type RepoLog struct {
	Repo RepoLogInfo `toml:"repo"`
}

type RepoLogInfo struct {
	Name    string `toml:"name"`
	Added   string `toml:"added"`
	AddedBy string `toml:"added_by"`
}

// --- Path helpers ---

func ExpandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// ContractPath replaces the home directory with ~ for display.
func ContractPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(p, home+sep) {
		return "~/" + filepath.ToSlash(p[len(home)+1:])
	}
	return p
}

// ConfigDir returns ~/.config/dotkeeper/
func ConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "dotkeeper")
}

// DataDir returns ~/.local/share/dotkeeper/
func DataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "dotkeeper")
}

func STConfigDir() string { return filepath.Join(DataDir(), "syncthing") }
func STDataDir() string   { return filepath.Join(DataDir(), "syncthing-data") }

func MachineConfigPath() string { return filepath.Join(ConfigDir(), "machine.toml") }
func SharedConfigPath() string  { return filepath.Join(ConfigDir(), "config.toml") }

// --- Load/write machine.toml ---

func LoadMachineConfig() (*MachineConfig, error) {
	path := MachineConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var cfg MachineConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &cfg, nil
}

func WriteMachineConfig(name string, slot int) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("# dotkeeper machine identity (local to this machine)\nname = %q\nslot = %d\n", name, slot)
	return os.WriteFile(MachineConfigPath(), []byte(content), 0o600)
}

// --- Load/write config.toml (shared) ---

func LoadSharedConfig() (*SharedConfig, error) {
	path := SharedConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var cfg SharedConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	if cfg.Sync.SlotOffsetMinutes == 0 {
		cfg.Sync.SlotOffsetMinutes = 5
	}
	if cfg.Sync.GitInterval == "" {
		cfg.Sync.GitInterval = "daily"
	}
	if cfg.Machines == nil {
		cfg.Machines = make(map[string]MachineEntry)
	}
	return &cfg, nil
}

// sanitizeTOMLKey ensures a key is valid UTF-8 for TOML serialization.
// Replaces invalid bytes with underscores.
func sanitizeTOMLKey(key string) string {
	if utf8.ValidString(key) {
		return key
	}
	var b strings.Builder
	for i := 0; i < len(key); {
		r, size := utf8.DecodeRuneInString(key[i:])
		if r == utf8.RuneError && size <= 1 {
			b.WriteByte('_')
			i++
		} else {
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// WriteSharedConfig writes config.toml. Generates TOML manually for readability.
func WriteSharedConfig(cfg *SharedConfig) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# dotkeeper shared config — synced between machines via Syncthing\n\n")

	b.WriteString("[sync]\n")
	b.WriteString(fmt.Sprintf("git_interval = %q\n", cfg.Sync.GitInterval))
	b.WriteString(fmt.Sprintf("slot_offset_minutes = %d\n", cfg.Sync.SlotOffsetMinutes))

	b.WriteString("\n[syncthing]\n")
	b.WriteString("ignore = [\n")
	for _, p := range cfg.Syncthing.Ignore {
		b.WriteString(fmt.Sprintf("    %q,\n", p))
	}
	b.WriteString("]\n")

	b.WriteString("\n[machines]\n")
	for name, m := range cfg.Machines {
		b.WriteString(fmt.Sprintf("\n[machines.%q]\n", sanitizeTOMLKey(name)))
		b.WriteString(fmt.Sprintf("hostname = %q\n", m.Hostname))
		b.WriteString(fmt.Sprintf("slot = %d\n", m.Slot))
		b.WriteString(fmt.Sprintf("syncthing_id = %q\n", m.SyncthingID))
	}

	if len(cfg.Repos) > 0 {
		b.WriteString("\n")
		for _, r := range cfg.Repos {
			b.WriteString("[[repos]]\n")
			b.WriteString(fmt.Sprintf("name = %q\n", r.Name))
			b.WriteString(fmt.Sprintf("path = %q\n", r.Path))
			b.WriteString(fmt.Sprintf("git = %v\n", r.Git))
			b.WriteString("\n")
		}
	}

	return os.WriteFile(SharedConfigPath(), []byte(b.String()), 0o600)
}

// AddMachine adds or updates a machine entry in the shared config.
func AddMachine(cfg *SharedConfig, key, hostname string, slot int, syncthingID string) {
	cfg.Machines[key] = MachineEntry{
		Hostname:    hostname,
		Slot:        slot,
		SyncthingID: syncthingID,
	}
}

// AddRepo adds a repo entry to the shared config if not already present.
// Returns true if added, false if already exists.
func AddRepo(cfg *SharedConfig, name, path string, git bool) bool {
	for _, r := range cfg.Repos {
		if r.Name == name {
			return false
		}
	}
	cfg.Repos = append(cfg.Repos, RepoEntry{Name: name, Path: path, Git: git})
	return true
}

// RemoveRepo removes a repo entry by name. Returns true if found and removed.
func RemoveRepo(cfg *SharedConfig, name string) bool {
	for i, r := range cfg.Repos {
		if r.Name == name {
			cfg.Repos = append(cfg.Repos[:i], cfg.Repos[i+1:]...)
			return true
		}
	}
	return false
}

// --- Per-repo log (dotkeeper.toml in each repo) ---

func LoadRepoLog(repoPath string) (*RepoLog, error) {
	path := filepath.Join(repoPath, "dotkeeper.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var log RepoLog
	if _, err := toml.DecodeFile(path, &log); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &log, nil
}

func WriteRepoLog(repoPath string, log *RepoLog) error {
	var b strings.Builder
	b.WriteString("# Managed by dotkeeper — https://github.com/julian-corbet/dotkeeper\n")
	b.WriteString("# This file is tracked in git for resilience.\n\n")

	b.WriteString("[repo]\n")
	b.WriteString(fmt.Sprintf("name = %q\n", log.Repo.Name))
	b.WriteString(fmt.Sprintf("added = %q\n", log.Repo.Added))
	b.WriteString(fmt.Sprintf("added_by = %q\n", log.Repo.AddedBy))

	path := filepath.Join(repoPath, "dotkeeper.toml")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// CreateRepoLog creates a new dotkeeper.toml in a repo.
func CreateRepoLog(repoPath, repoName, machineName string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	log := &RepoLog{
		Repo: RepoLogInfo{
			Name:    repoName,
			Added:   now,
			AddedBy: machineName,
		},
	}
	return WriteRepoLog(repoPath, log)
}

// --- Default ignore patterns ---

func DefaultIgnorePatterns() []string {
	return []string{
		".git",
		"*.sync-conflict-*",
		".dkfolder",
		".stignore",
		"(?d).DS_Store",
		"(?d)Thumbs.db",
		"(?d)desktop.ini",
		"*-bin",
		"*.exe",
		"*.dll",
		"*.so",
		"*.dylib",
		"*.o",
		"*.a",
		"__debug_bin*",
		"/target",
		"/dist",
		"/build",
		"/out",
		"node_modules",
		".venv",
		"venv",
		"__pycache__",
		"*.pyc",
		"*.pyo",
		".eggs",
		"*.egg-info",
		"*.sqlite3",
		"*.sqlite3-journal",
		"*.sqlite3-wal",
		"*.sqlite3-shm",
		"*.sqlite",
		"*.sqlite-journal",
		"*.sqlite-wal",
		"*.sqlite-shm",
		"*.pid",
		"*.sock",
		"*.log",
		"*.log.*",
		"*.swp",
		"*.swo",
		".*.swp",
		".*.swo",
		"*~",
		"#*#",
		".idea/workspace.xml",
		".idea/tasks.xml",
		".idea/usage.statistics.xml",
		".idea/shelf",
		".idea/caches",
		".idea/*.iws",
		".vscode/*.log",
		".cache",
		".parcel-cache",
		".next",
		".nuxt",
		".turbo",
		".angular",
		".sass-cache",
		".pytest_cache",
		".mypy_cache",
		".ruff_cache",
		".tox",
		".gradle",
		"coverage",
		"htmlcov",
		"*.tmp",
		"*.temp",
		"*.bak",
		"*.orig",
	}
}


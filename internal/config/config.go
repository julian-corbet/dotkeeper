// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package config handles dotkeeper configuration loading and path management.
//
// Two config files used in v0.5+:
//   - machine.toml  — local identity (name, slot, scan roots). Never synced.
//   - state.toml    — runtime state (device ID, peers, tracked repos). Never synced.
//
// Per-repo: dotkeeper.toml in each managed repo (tracked in git).
package config

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

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

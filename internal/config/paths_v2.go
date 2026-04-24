// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
)

// StateDir returns the XDG state directory for dotkeeper:
// $XDG_STATE_HOME/dotkeeper, falling back to $HOME/.local/state/dotkeeper.
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dotkeeper")
}

// StateV2Path returns the full path to state.toml.
func StateV2Path() string {
	return filepath.Join(StateDir(), "state.toml")
}

// RepoConfigPath returns the path to the per-repo dotkeeper.toml for the
// given repo root.
func RepoConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, "dotkeeper.toml")
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package discovery provides shared helpers for finding managed dotkeeper
// repos on disk. Both the main CLI (startConflictWatcher, managedFolderPathsV5)
// and the doctor package's ConflictsCheck use this logic, so it lives here to
// avoid duplication.
package discovery

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// WalkScanRoot recursively walks root up to maxDepth directory levels looking
// for directories that contain a dotkeeper.toml file. When a directory with
// dotkeeper.toml is found, fn is called with its absolute path.
//
// Canonical behaviour: the walk stops descending into any directory that
// itself contains a dotkeeper.toml. This is intentional — that directory is a
// managed repo root, and walking into it would be wasteful (nested repos are
// not supported; any dotkeeper.toml deeper than the root is the repo's own
// config, not a new managed repo).
//
// Hidden directories (names beginning with ".") are skipped.
func WalkScanRoot(root string, depth, maxDepth int, fn func(string)) error {
	if depth > maxDepth {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	// Check whether this directory is itself a managed repo root.
	for _, e := range entries {
		if !e.IsDir() && e.Name() == "dotkeeper.toml" {
			fn(root)
			return nil // do not descend into a managed repo
		}
	}

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		_ = WalkScanRoot(filepath.Join(root, e.Name()), depth+1, maxDepth, fn)
	}
	return nil
}

// ManagedFolderPaths returns absolute, existing paths for every managed folder
// discoverable from v0.5 state: the config directory, TrackedOverrides from
// state.toml, and any repo roots found by walking scan_roots in machine.toml.
//
// ObservedRepos is intentionally NOT consulted: if a scan_root is removed from
// machine.toml, repos under it should become invisible to callers, and the
// next reconcile pass will prune those entries from ObservedRepos.
//
// Missing paths are silently skipped.
func ManagedFolderPaths() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(config.ExpandPath(p))
		if err != nil {
			return
		}
		if _, err := os.Stat(abs); err != nil {
			return
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}

	// Always include the config directory.
	add(config.ConfigDir())

	// Add TrackedOverrides from state.toml.
	if state, err := config.LoadStateV2(); err == nil && state != nil {
		for _, p := range state.TrackedOverrides {
			add(p)
		}
	}

	// Walk scan roots to find repos with dotkeeper.toml.
	if machine, err := config.LoadMachineConfigV2(); err == nil && machine != nil {
		for _, root := range machine.Discovery.ScanRoots {
			expanded := config.ExpandPath(root)
			if info, err := os.Stat(expanded); err != nil || !info.IsDir() {
				continue
			}
			walkDepth := machine.Discovery.ScanDepth
			if walkDepth <= 0 {
				walkDepth = 3
			}
			_ = WalkScanRoot(expanded, 0, walkDepth, func(repoPath string) {
				add(repoPath)
			})
		}
	}

	return out
}

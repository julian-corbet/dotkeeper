// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func TestMergeSyncIgnorePatternsIncludesDefaultsAndExtras(t *testing.T) {
	t.Parallel()

	got := MergeSyncIgnorePatterns([]string{"node_modules", "custom-cache", ""})
	for _, want := range []string{".git", RepoConfigFileName, "dotkeeper.toml", ".stignore", "node_modules", "dist", "custom-cache"} {
		if !containsPattern(got, want) {
			t.Fatalf("merged patterns missing %q: %v", want, got)
		}
	}

	count := 0
	for _, pattern := range got {
		if pattern == "node_modules" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("node_modules should appear once, got %d entries in %v", count, got)
	}
}

func TestSyncIgnoreFileContentStable(t *testing.T) {
	t.Parallel()

	got := SyncIgnoreFileContent([]string{"custom-cache"})
	if !strings.HasPrefix(got, "# Managed by dotkeeper") {
		t.Fatalf("missing managed header: %q", got)
	}
	for _, want := range []string{".git\n", RepoConfigFileName + "\n", "dotkeeper.toml\n", ".stignore\n", "node_modules\n", "custom-cache\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ignore content missing %q:\n%s", want, got)
		}
	}
}

// TestDefaultSyncIgnorePatternsCoversLanguageServerCaches pins the
// presence of language-server and tooling caches added in v0.9.2 after
// they were observed dominating Syncthing's index/rescan footprint on
// active development trees. Adding new caches is fine; removing one
// should be a deliberate decision, not an accidental refactor.
func TestDefaultSyncIgnorePatternsCoversLanguageServerCaches(t *testing.T) {
	t.Parallel()

	required := []string{
		".zig-cache",
		".rust-analyzer",
		".ccls-cache",
		".clangd",
		".ipynb_checkpoints",
		"playwright-report",
		"test-results",
	}
	for _, want := range required {
		if !containsPattern(DefaultSyncIgnorePatterns, want) {
			t.Fatalf("DefaultSyncIgnorePatterns missing %q", want)
		}
	}
}

// TestDefaultSyncIgnorePatternsCoversAIAgentEphemera pins the
// presence of agent-related ephemera that, when left to Syncthing,
// produce log-spam on every peer. `.claude/worktrees/` is the
// canonical example: Claude Code creates one transient nested-git
// worktree per agent run; without this ignore entry Syncthing
// tries to replicate them across peers and then perpetually flaps
// on "delete directory contains ignored files" warnings every time
// an agent finishes locally. Future agent tools that create
// repo-local ephemera belong here too.
func TestDefaultSyncIgnorePatternsCoversAIAgentEphemera(t *testing.T) {
	t.Parallel()

	required := []string{
		".claude/worktrees",
	}
	for _, want := range required {
		if !containsPattern(DefaultSyncIgnorePatterns, want) {
			t.Fatalf("DefaultSyncIgnorePatterns missing %q — agent worktrees must not propagate via Syncthing", want)
		}
	}
}

// TestDefaultSyncIgnorePatternsAreConsolidated guards the v0.9.3
// pattern-consolidation work against accidental re-expansion.
// Syncthing's ignore matcher pays a per-pattern cost dominated by
// glob matching; the prior default list enumerated several variant
// families (sqlite, swap, pyc, log) that all collapse to a single
// glob each.
//
// Each "absent" check fails if anyone reintroduces an enumerated
// variant; each "present" check confirms the consolidating glob is
// still in place. If a future change has a documented reason to
// split a family back out (e.g. a Syncthing matcher bug with a
// specific glob class), update this test deliberately with a
// comment — don't silently delete the guard.
func TestDefaultSyncIgnorePatternsAreConsolidated(t *testing.T) {
	t.Parallel()

	// Patterns that MUST be present — the consolidating globs.
	required := []string{
		"*.sqlite*",
		"*.log*",
		"*.py[co]",
		"*.sw[op]",
		".*.sw[op]",
	}
	for _, want := range required {
		if !containsPattern(DefaultSyncIgnorePatterns, want) {
			t.Errorf("consolidated pattern %q missing; was it split back into variants?", want)
		}
	}

	// Patterns that MUST be absent — the old enumerated variants.
	forbidden := []string{
		"*.sqlite3",
		"*.sqlite3-journal",
		"*.sqlite3-wal",
		"*.sqlite3-shm",
		"*.sqlite",
		"*.sqlite-journal",
		"*.sqlite-wal",
		"*.sqlite-shm",
		"*.log.*",
		"*.pyc",
		"*.pyo",
		"*.swp",
		"*.swo",
		".*.swp",
		".*.swo",
	}
	for _, gone := range forbidden {
		if containsPattern(DefaultSyncIgnorePatterns, gone) {
			t.Errorf("pre-consolidation variant %q reintroduced; collapse into the family glob instead", gone)
		}
	}
}

func containsPattern(patterns []string, want string) bool {
	for _, pattern := range patterns {
		if pattern == want {
			return true
		}
	}
	return false
}

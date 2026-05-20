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

func containsPattern(patterns []string, want string) bool {
	for _, pattern := range patterns {
		if pattern == want {
			return true
		}
	}
	return false
}

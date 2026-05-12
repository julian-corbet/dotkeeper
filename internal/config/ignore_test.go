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

func containsPattern(patterns []string, want string) bool {
	for _, pattern := range patterns {
		if pattern == want {
			return true
		}
	}
	return false
}

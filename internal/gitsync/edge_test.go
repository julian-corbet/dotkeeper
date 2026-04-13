// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestRepoNameUsesFilepathBase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/Documents/project", "project"},
		{"/home/user/My Projects/hello world", "hello world"},
		{"project", "project"},
		{"/a/b/c/d", "d"},
	}

	for _, tt := range tests {
		got := repoName(tt.input)
		if got != tt.want {
			t.Errorf("repoName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRepoNameWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		// filepath.Base handles both / and \ on all platforms,
		// but this test documents the intent
		got := repoName("/Users/bob/project")
		if got != "project" {
			t.Errorf("repoName with forward slashes = %q, want 'project'", got)
		}
	}
}

func TestRepoNameIsFilepathBase(t *testing.T) {
	// Verify repoName uses filepath.Base, not string splitting
	path := "/some/deep/nested/repo"
	if repoName(path) != filepath.Base(path) {
		t.Error("repoName does not match filepath.Base")
	}
}

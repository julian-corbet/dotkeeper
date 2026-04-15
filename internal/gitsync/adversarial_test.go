// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncRepoDoesNotPushWhenSynced verifies that SyncRepo does NOT push
// when local and remote are already in sync (ahead == 0).
// Targets surviving mutant at sync.go:53 (ahead > 0 boundary).
func TestSyncRepoDoesNotPushWhenSynced(t *testing.T) {
	work := setupGitRepo(t)

	// Get the current remote HEAD
	cmd := exec.Command("git", "rev-parse", "origin/main")
	cmd.Dir = work
	beforePush, _ := cmd.Output()

	// Sync with no local changes — should NOT push
	if err := SyncRepo(work, "test-machine"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// Remote HEAD should be unchanged
	cmd = exec.Command("git", "rev-parse", "origin/main")
	cmd.Dir = work
	afterPush, _ := cmd.Output()

	if string(beforePush) != string(afterPush) {
		t.Error("SyncRepo pushed when already in sync (ahead == 0)")
	}
}

// TestSyncRepoEmptyRepo verifies behavior with an empty repo (no commits yet).
func TestSyncRepoEmptyRepo(t *testing.T) {
	tmp := t.TempDir()
	work := filepath.Join(tmp, "empty")

	cmd := exec.Command("git", "init", work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// SyncRepo on a repo with no remote should fail gracefully
	err := SyncRepo(work, "test-machine")
	if err == nil {
		t.Fatal("expected error for repo with no remote")
	}
}

// TestSyncRepoSpacesInPath verifies git sync works when the repo path has spaces.
func TestSyncRepoSpacesInPath(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote repo.git")
	work := filepath.Join(tmp, "my work dir")

	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)

	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v in %q failed: %v\n%s", name, args, dir, err, out)
		}
	}

	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work)
	run(work, "git", "checkout", "-b", "main")

	os.WriteFile(filepath.Join(work, "README.md"), []byte("# test\n"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-m", "initial")
	run(work, "git", "push", "-u", "origin", "main")

	// Create a file and sync
	os.WriteFile(filepath.Join(work, "data.txt"), []byte("hello\n"), 0o644)
	if err := SyncRepo(work, "test-machine"); err != nil {
		t.Fatalf("SyncRepo with spaces in path: %v", err)
	}

	// Verify push happened
	cmd := exec.Command("git", "log", "--oneline", "origin/main", "-1")
	cmd.Dir = work
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "auto:") {
		t.Errorf("commit not pushed: %s", out)
	}
}

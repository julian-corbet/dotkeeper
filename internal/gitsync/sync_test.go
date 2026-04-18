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

// setupGitRepo creates a bare remote + working clone for testing.
// Returns (workDir, cleanup).
func setupGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	// Create bare remote
	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}

	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work)
	run(work, "git", "checkout", "-b", "main")

	// Initial commit
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# test\n"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-m", "initial")
	run(work, "git", "push", "-u", "origin", "main")

	return work
}

func TestSyncRepoNoChanges(t *testing.T) {
	work := setupGitRepo(t)

	err := SyncRepo(work, "test-machine")
	if err != nil {
		t.Fatalf("SyncRepo with no changes: %v", err)
	}
}

func TestSyncRepoWithChanges(t *testing.T) {
	work := setupGitRepo(t)

	// Create a new file
	_ = os.WriteFile(filepath.Join(work, "new-file.txt"), []byte("hello\n"), 0o644)

	err := SyncRepo(work, "test-machine")
	if err != nil {
		t.Fatalf("SyncRepo with changes: %v", err)
	}

	// Verify commit was made
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = work
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "auto: test-machine") {
		t.Errorf("expected auto-commit message, got: %s", out)
	}

	// Verify push happened (remote should have the commit)
	cmd = exec.Command("git", "log", "--oneline", "origin/main", "-1")
	cmd.Dir = work
	out, _ = cmd.Output()
	if !strings.Contains(string(out), "auto: test-machine") {
		t.Errorf("commit not pushed, remote log: %s", out)
	}
}

func TestSyncRepoMultipleFiles(t *testing.T) {
	work := setupGitRepo(t)

	// Create multiple files
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "b.txt"), []byte("b\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(work, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(work, "subdir/c.txt"), []byte("c\n"), 0o644)

	err := SyncRepo(work, "test-machine")
	if err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// All files should be committed
	cmd := exec.Command("git", "diff", "--stat", "HEAD~1")
	cmd.Dir = work
	out, _ := cmd.Output()
	s := string(out)
	if !strings.Contains(s, "a.txt") || !strings.Contains(s, "b.txt") || !strings.Contains(s, "subdir/c.txt") {
		t.Errorf("not all files committed, diff: %s", s)
	}
}

func TestSyncRepoNotAGitRepo(t *testing.T) {
	tmp := t.TempDir()

	err := SyncRepo(tmp, "test-machine")
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSyncRepoRemoteUnreachable(t *testing.T) {
	work := setupGitRepo(t)

	// Break the remote
	cmd := exec.Command("git", "remote", "set-url", "origin", "/nonexistent/path")
	cmd.Dir = work
	_ = cmd.Run()

	err := SyncRepo(work, "test-machine")
	if err == nil {
		t.Fatal("expected error for unreachable remote")
	}
	if !strings.Contains(err.Error(), "remote unreachable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSyncRepoRespectGitignore(t *testing.T) {
	work := setupGitRepo(t)

	// Create .gitignore
	_ = os.WriteFile(filepath.Join(work, ".gitignore"), []byte("*.log\nsecret.env\n"), 0o644)
	// Create files — one tracked, one ignored
	_ = os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("tracked\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "debug.log"), []byte("ignored\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "secret.env"), []byte("PASSWORD=x\n"), 0o644)

	err := SyncRepo(work, "test-machine")
	if err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// Check what was committed
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = work
	out, _ := cmd.Output()
	files := string(out)

	if !strings.Contains(files, "tracked.txt") {
		t.Error("tracked.txt should be committed")
	}
	if strings.Contains(files, "debug.log") {
		t.Error("debug.log should be ignored")
	}
	if strings.Contains(files, "secret.env") {
		t.Error("secret.env should be ignored")
	}
}

func TestSyncRepoIdempotent(t *testing.T) {
	work := setupGitRepo(t)

	// First sync — no changes
	_ = SyncRepo(work, "test-machine")

	// Count commits
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = work
	out1, _ := cmd.Output()

	// Second sync — still no changes
	_ = SyncRepo(work, "test-machine")

	cmd = exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = work
	out2, _ := cmd.Output()

	if string(out1) != string(out2) {
		t.Errorf("sync without changes created commits: %s → %s", strings.TrimSpace(string(out1)), strings.TrimSpace(string(out2)))
	}
}

func TestSyncRepoPullsRemoteChanges(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work1 := filepath.Join(tmp, "work1")
	work2 := filepath.Join(tmp, "work2")

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
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}

	// Set up shared remote + two clones
	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work1)
	run(work1, "git", "checkout", "-b", "main")
	_ = os.WriteFile(filepath.Join(work1, "README.md"), []byte("# test\n"), 0o644)
	run(work1, "git", "add", ".")
	run(work1, "git", "commit", "-m", "initial")
	run(work1, "git", "push", "-u", "origin", "main")
	run(tmp, "git", "clone", bare, work2)
	run(work2, "git", "checkout", "main")

	// Commit on work1 and push
	_ = os.WriteFile(filepath.Join(work1, "from-work1.txt"), []byte("from work1\n"), 0o644)
	run(work1, "git", "add", ".")
	run(work1, "git", "commit", "-m", "from work1")
	run(work1, "git", "push")

	// Sync on work2 — should pull work1's changes
	err := SyncRepo(work2, "work2-machine")
	if err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// Verify work1's file is now in work2
	if _, err := os.Stat(filepath.Join(work2, "from-work1.txt")); os.IsNotExist(err) {
		t.Error("work2 did not pull from-work1.txt")
	}
}

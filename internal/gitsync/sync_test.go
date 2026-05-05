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

	// Verify commit was made with the anonymous fixed message: no
	// hostname, no timestamp, no other identifier.
	cmd := exec.Command("git", "log", "--format=%s", "-1")
	cmd.Dir = work
	out, _ := cmd.Output()
	subject := strings.TrimSpace(string(out))
	if subject != "auto: scheduled backup" {
		t.Errorf("commit subject = %q, want %q", subject, "auto: scheduled backup")
	}
	if strings.Contains(subject, "test-machine") {
		t.Errorf("commit subject leaked machine name: %q", subject)
	}

	// Verify the same anonymous subject was pushed to the remote.
	cmd = exec.Command("git", "log", "--format=%s", "origin/main", "-1")
	cmd.Dir = work
	out, _ = cmd.Output()
	if got := strings.TrimSpace(string(out)); got != "auto: scheduled backup" {
		t.Errorf("remote subject = %q, want %q", got, "auto: scheduled backup")
	}

	// Verify the local-only git note carries the machine name and time.
	// refs/notes/dotkeeper is not pushed by default, so the debug info
	// stays on the originating machine.
	cmd = exec.Command("git", "notes", "--ref=dotkeeper", "show", "HEAD")
	cmd.Dir = work
	noteOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("expected git note on HEAD, got error: %v", err)
	}
	note := string(noteOut)
	if !strings.Contains(note, "machine=test-machine") {
		t.Errorf("note missing machine=test-machine, got: %q", note)
	}
	if !strings.Contains(note, "time=") {
		t.Errorf("note missing time=, got: %q", note)
	}

	// Verify the note was NOT pushed to the remote.
	cmd = exec.Command("git", "ls-remote", "origin", "refs/notes/dotkeeper")
	cmd.Dir = work
	remoteRefs, _ := cmd.Output()
	if strings.TrimSpace(string(remoteRefs)) != "" {
		t.Errorf("refs/notes/dotkeeper leaked to remote: %q", remoteRefs)
	}
}

// TestSyncRepoSkipsAccidentalDeletions verifies the safeguard that
// dotkeeper auto-commit must not propagate file deletions caused by plain
// `rm`, filesystem faults, or sync glitches. Only deletions explicitly
// staged by the user via `git rm` may flow through.
func TestSyncRepoSkipsAccidentalDeletions(t *testing.T) {
	work := setupGitRepo(t)

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Set up: tracked file with content, committed and pushed.
	p := filepath.Join(work, "important.txt")
	if err := os.WriteFile(p, []byte("important data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "important.txt")
	runGit("commit", "-m", "add important")
	runGit("push")

	// Simulate accidental deletion: plain `rm`, NOT `git rm`.
	if err := os.Remove(p); err != nil {
		t.Fatalf("os.Remove: %v", err)
	}

	// Run sync. Must not propagate the deletion.
	if err := SyncRepo(work, "test-machine"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// HEAD must still contain the file — the deletion should never have
	// been committed.
	cmd := exec.Command("git", "ls-tree", "HEAD", "important.txt")
	cmd.Dir = work
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) == "" {
		t.Fatalf("safeguard failed: deletion was committed despite being unintended")
	}
}

// TestSyncRepoCommitsIntentionalDeletions verifies the other side of the
// safeguard: a deletion the user explicitly staged via `git rm` flows
// through unchanged.
func TestSyncRepoCommitsIntentionalDeletions(t *testing.T) {
	work := setupGitRepo(t)

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	p := filepath.Join(work, "obsolete.txt")
	if err := os.WriteFile(p, []byte("temp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "obsolete.txt")
	runGit("commit", "-m", "add obsolete")
	runGit("push")

	// User explicitly stages the deletion with `git rm`.
	runGit("rm", "obsolete.txt")

	if err := SyncRepo(work, "test-machine"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// HEAD must NOT contain the file — the intentional deletion should
	// have been committed.
	cmd := exec.Command("git", "ls-tree", "HEAD", "obsolete.txt")
	cmd.Dir = work
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("intentional `git rm` deletion was not committed: %q", out)
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

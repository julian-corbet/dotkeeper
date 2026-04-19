// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitInit spins up a minimal git repo in dir and returns its absolute
// path. Uses an isolated config (GIT_CONFIG_GLOBAL=/dev/null) so the
// host's git config cannot leak into the test — commit.gpgsign, user
// identity, etc.
func gitInit(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	// -b main keeps the default-branch name stable regardless of the
	// host's init.defaultBranch setting, which makes the `git show HEAD`
	// calls deterministic.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = abs
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@dotkeeper.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@dotkeeper.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", ".")
	run("config", "user.name", "test")
	run("config", "user.email", "test@dotkeeper.invalid")
	// Some hosts default to commit.gpgsign=true; kill it so the tests
	// aren't at the mercy of a missing signing key.
	run("config", "commit.gpgsign", "false")
	return abs
}

// gitCommit writes relPath, stages it, and commits. Separate from
// gitInit because tests often want several commits on top of init.
func gitCommit(t *testing.T, repo, relPath, content, msg string) {
	t.Helper()
	full := filepath.Join(repo, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@dotkeeper.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@dotkeeper.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "--", relPath)
	run("commit", "-q", "-m", msg)
}

// testCtx returns a short-bounded context; any resolver call that hangs
// longer than this fails the test. Real resolves are sub-second.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestResolveTextMergeCleanMerge — non-overlapping edits on either side
// should merge automatically, delete the conflict, and produce an
// auto-commit that contains only the merged file.
func TestResolveTextMergeCleanMerge(t *testing.T) {
	repo := gitInit(t, t.TempDir())

	base := "line 1\nline 2\nline 3\n"
	gitCommit(t, repo, "hello.txt", base, "initial")

	// Local ("ours") edits line 3.
	local := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(local, []byte("line 1\nline 2\nline THREE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Conflict ("theirs") edits line 1. Non-overlapping.
	c := makeConflict(t, repo, "hello.txt")
	if err := os.WriteFile(c.Path, []byte("line ONE\nline 2\nline 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionMerged {
		t.Fatalf("Action = %q, want %q", got, ActionMerged)
	}

	merged, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	want := "line ONE\nline 2\nline THREE\n"
	if string(merged) != want {
		t.Errorf("merged content:\n%s\nwant:\n%s", merged, want)
	}

	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Errorf("conflict file should be gone: %v", err)
	}

	// Verify the auto-commit exists and is scoped to hello.txt only.
	logCmd := exec.Command("git", "-C", repo, "log", "-1", "--name-only", "--pretty=format:%s")
	out, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "auto: resolve sync conflict in hello.txt") {
		t.Errorf("commit subject missing; log:\n%s", out)
	}
	if !strings.Contains(string(out), "hello.txt") {
		t.Errorf("commit does not touch hello.txt; log:\n%s", out)
	}
}

// TestResolveTextMergeScopedCommit — if the user has unrelated dirty
// changes in the tree, the auto-commit must NOT sweep them in. This is
// the "don't lose data silently" invariant.
func TestResolveTextMergeScopedCommit(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "hello.txt", "line 1\nline 2\nline 3\n", "initial")
	gitCommit(t, repo, "other.txt", "untouched\n", "add other")

	// Dirty, unstaged change on other.txt. Must not end up in our commit.
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("DIRTY\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up a clean-merge conflict on hello.txt.
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"),
		[]byte("line 1\nline 2\nline THREE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, repo, "hello.txt")
	if err := os.WriteFile(c.Path, []byte("line ONE\nline 2\nline 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionMerged {
		t.Fatalf("Action = %q, want %q", got, ActionMerged)
	}

	// The last commit must touch hello.txt and nothing else.
	out, err := exec.Command("git", "-C", repo,
		"show", "--name-only", "--pretty=format:", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, out)
	}
	files := strings.Fields(string(out))
	if len(files) != 1 || files[0] != "hello.txt" {
		t.Errorf("commit touched %v, want only [hello.txt]", files)
	}

	// other.txt should still be dirty unstaged — we never touched it.
	got2, err := os.ReadFile(filepath.Join(repo, "other.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "DIRTY\n" {
		t.Errorf("other.txt content changed: %q", got2)
	}
}

// TestResolveTextMergeConflictMarkers — overlapping edits produce a
// merge with conflict markers. We refuse: both files stay in place so
// the user can resolve with their preferred tool.
func TestResolveTextMergeConflictMarkers(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "hello.txt", "original line\n", "initial")

	local := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(local, []byte("ours line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, repo, "hello.txt")
	if err := os.WriteFile(c.Path, []byte("theirs line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionKeep {
		t.Fatalf("Action = %q, want %q", got, ActionKeep)
	}
	// Local file must be unchanged — definitely no markers written.
	b, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "ours line\n" {
		t.Errorf("local was modified: %q", b)
	}
	if _, err := os.Stat(c.Path); err != nil {
		t.Errorf("conflict file should still exist: %v", err)
	}
}

// TestResolveTextMergeBinaryKept — a file with a NUL in the first 8KB
// must bypass the merge and return ActionKeep. Binary merge is out of
// scope for Phase 2.
func TestResolveTextMergeBinaryKept(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "img.dat", "abc\x00def", "initial")

	local := filepath.Join(repo, "img.dat")
	if err := os.WriteFile(local, []byte("abc\x00def-OURS"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, repo, "img.dat")
	if err := os.WriteFile(c.Path, []byte("abc\x00def-THEIRS"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionKeep {
		t.Errorf("Action = %q, want %q", got, ActionKeep)
	}
}

// TestResolveTextMergeFileNotInHEAD — a newly-added-but-never-committed
// file has no common ancestor. Can't 3-way merge without a base, so we
// escalate to the user.
func TestResolveTextMergeFileNotInHEAD(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	// Must have at least one commit so HEAD exists at all.
	gitCommit(t, repo, "placeholder.txt", "x\n", "initial")

	local := filepath.Join(repo, "fresh.txt")
	if err := os.WriteFile(local, []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, repo, "fresh.txt")
	if err := os.WriteFile(c.Path, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionKeep {
		t.Errorf("Action = %q, want %q", got, ActionKeep)
	}
}

// TestIsTextFileBoundary — a file with a NUL in the first 8KB is binary;
// a file with a NUL right after the window is text (we don't scan
// further, by design — matches git diff behaviour).
func TestIsTextFileBoundary(t *testing.T) {
	dir := t.TempDir()

	// Single NUL at byte 100 → binary.
	binPath := filepath.Join(dir, "bin.dat")
	buf := make([]byte, 500)
	for i := range buf {
		buf[i] = 'a'
	}
	buf[100] = 0
	if err := os.WriteFile(binPath, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := isTextFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("file with NUL at byte 100 reported as text")
	}

	// NUL at byte 20000 (well past the 8KB window) → considered text.
	okPath := filepath.Join(dir, "long.txt")
	long := make([]byte, 30000)
	for i := range long {
		long[i] = 'a'
	}
	long[20000] = 0
	if err := os.WriteFile(okPath, long, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = isTextFile(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("file with NUL at byte 20000 reported as binary; window is 8KB")
	}
}

// TestResolveTextMergePreservesMode — merged files keep their original
// permission bits (e.g. +x on shell scripts). Regression guard against
// a naive os.WriteFile that would drop the mode.
func TestResolveTextMergePreservesMode(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "run.sh", "#!/bin/sh\nA\nB\nC\nD\n", "initial")

	local := filepath.Join(repo, "run.sh")
	if err := os.WriteFile(local, []byte("#!/bin/sh\nA\nB\nC\nD_local\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(local, 0o755); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, repo, "run.sh")
	if err := os.WriteFile(c.Path, []byte("#!/bin/sh\nA_theirs\nB\nC\nD\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTextMerge(testCtx(t), c, repo)
	if err != nil {
		t.Fatalf("ResolveTextMerge: %v", err)
	}
	if got != ActionMerged {
		t.Fatalf("merge did not come out clean: %s", got)
	}
	info, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("executable bit lost after merge: mode = %v", info.Mode().Perm())
	}
}

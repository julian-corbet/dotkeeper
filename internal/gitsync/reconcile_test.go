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

// twoClones builds a bare remote and two working clones sharing it. Returns
// (work1, work2). Both clones already have a shared initial commit on main.
func twoClones(t *testing.T) (string, string) {
	t.Helper()
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
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}

	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work1)
	run(work1, "git", "checkout", "-b", "main")
	_ = os.WriteFile(filepath.Join(work1, "README.md"), []byte("# test\n"), 0o644)
	run(work1, "git", "add", ".")
	run(work1, "git", "commit", "-m", "initial")
	run(work1, "git", "push", "-u", "origin", "main")
	run(tmp, "git", "clone", bare, work2)
	run(work2, "git", "checkout", "main")

	return work1, work2
}

// mustRun runs a git command and fails the test on error.
func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestReconcile_UntrackedMatchingUpstream models the exact failure we saw:
// peer created a new file and pushed. Syncthing delivered the same content to
// our working tree before we pulled, so the file is present locally as
// untracked while the incoming commit wants to add it. Reconcile should
// detect the match and let the pull restore the file from the blob, turning
// a hard error into a clean fast-forward.
func TestReconcile_UntrackedMatchingUpstream(t *testing.T) {
	work1, work2 := twoClones(t)

	// work1 adds a file and pushes.
	_ = os.WriteFile(filepath.Join(work1, "new.txt"), []byte("shared content\n"), 0o644)
	mustRun(t, work1, "add", "new.txt")
	mustRun(t, work1, "commit", "-m", "add new.txt")
	mustRun(t, work1, "push")

	// Simulate Syncthing: the identical file appears on work2 as untracked
	// BEFORE work2 pulls. Without reconcile, `git pull` aborts here.
	_ = os.WriteFile(filepath.Join(work2, "new.txt"), []byte("shared content\n"), 0o644)

	if err := SyncRepo(work2, "work2"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	// File must now be tracked, working tree clean, HEAD advanced.
	assertClean(t, work2)
	if _, err := os.Stat(filepath.Join(work2, "new.txt")); err != nil {
		t.Fatalf("new.txt missing after reconcile: %v", err)
	}
	if !headContains(t, work2, "add new.txt") {
		t.Fatalf("HEAD did not advance to upstream commit")
	}
}

// TestReconcile_ModifiedMatchingUpstream covers the tracked-file variant:
// peer modified an existing file and pushed. Syncthing delivered the edit to
// our working tree so the file shows as locally modified, but its content
// matches the incoming commit. Reconcile should clear the local mod and let
// pull fast-forward.
func TestReconcile_ModifiedMatchingUpstream(t *testing.T) {
	work1, work2 := twoClones(t)

	// Both clones start with README.md; work1 edits it and pushes.
	_ = os.WriteFile(filepath.Join(work1, "README.md"), []byte("# edited by work1\n"), 0o644)
	mustRun(t, work1, "add", "README.md")
	mustRun(t, work1, "commit", "-m", "edit README")
	mustRun(t, work1, "push")

	// Syncthing delivers the same content to work2's working tree.
	_ = os.WriteFile(filepath.Join(work2, "README.md"), []byte("# edited by work1\n"), 0o644)

	if err := SyncRepo(work2, "work2"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	assertClean(t, work2)
	if !headContains(t, work2, "edit README") {
		t.Fatalf("HEAD did not advance to upstream commit")
	}
}

// TestReconcile_PreservesGenuineLocalChanges ensures reconcile only touches
// files whose content already matches upstream. A genuine local edit must
// survive and be committed normally.
func TestReconcile_PreservesGenuineLocalChanges(t *testing.T) {
	work1, work2 := twoClones(t)

	// work1 pushes an upstream change we're about to race with.
	_ = os.WriteFile(filepath.Join(work1, "shared.txt"), []byte("from work1\n"), 0o644)
	mustRun(t, work1, "add", "shared.txt")
	mustRun(t, work1, "commit", "-m", "add shared.txt")
	mustRun(t, work1, "push")

	// Syncthing delivers shared.txt to work2's working tree (matching content).
	_ = os.WriteFile(filepath.Join(work2, "shared.txt"), []byte("from work1\n"), 0o644)
	// Meanwhile, work2 has its own genuine local edit in another file.
	_ = os.WriteFile(filepath.Join(work2, "local.txt"), []byte("work2-only\n"), 0o644)

	if err := SyncRepo(work2, "work2"); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	assertClean(t, work2)
	if _, err := os.Stat(filepath.Join(work2, "local.txt")); err != nil {
		t.Fatalf("local.txt lost: %v", err)
	}
	if !headContains(t, work2, "add shared.txt") {
		t.Fatalf("HEAD did not include upstream commit")
	}
	// local.txt should have been committed by work2.
	if !headContains(t, work2, "auto: work2") {
		t.Fatalf("work2's local edit was not committed")
	}
}

// TestReconcile_LeavesDivergingContentAlone guards against overzealous
// reconciliation: if the local file differs from both HEAD and upstream, it's
// a genuine conflict and reconcile must not silently drop the local copy.
// Asserts the divergent content is still present on disk afterwards — the
// subsequent pull may fall back to autostash conflict markers, which is fine:
// the point is that reconcile itself didn't overwrite local work.
func TestReconcile_LeavesDivergingContentAlone(t *testing.T) {
	work1, work2 := twoClones(t)

	_ = os.WriteFile(filepath.Join(work1, "README.md"), []byte("# from work1\n"), 0o644)
	mustRun(t, work1, "add", "README.md")
	mustRun(t, work1, "commit", "-m", "work1 edits README")
	mustRun(t, work1, "push")

	// Genuine diverging edit on work2: different content than upstream.
	divergent := "# locally different on work2"
	_ = os.WriteFile(filepath.Join(work2, "README.md"), []byte(divergent+"\n"), 0o644)

	// Run reconcile directly — we're asserting its isolated behavior.
	_ = reconcileSyncthingDelivery(work2)

	got, err := os.ReadFile(filepath.Join(work2, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	if !strings.Contains(string(got), divergent) {
		t.Fatalf("reconcile dropped divergent local content: got %q", got)
	}
}

// assertClean fails if the working tree has any pending changes.
func assertClean(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected clean working tree, got:\n%s", out)
	}
}

// headContains reports whether any commit reachable from HEAD has a message
// containing the substring.
func headContains(t *testing.T, dir, needle string) bool {
	t.Helper()
	cmd := exec.Command("git", "log", "--pretty=%s")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return strings.Contains(string(out), needle)
}

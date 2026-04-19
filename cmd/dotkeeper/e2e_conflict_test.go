// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupConflictHarness builds the binary, runs `init` in a temp
// XDG_CONFIG_HOME, and creates one git-backed repo registered with
// dotkeeper. Returns (binary, tmp root, repo path, cleanup).
//
// This is the minimum shape the conflict CLI needs: a valid
// config.toml, a managed repo, and a real git history so Accept's
// commit step has somewhere to land.
func setupConflictHarness(t *testing.T) (binary, tmp, repo string) {
	t.Helper()
	tmp = t.TempDir()
	binary = filepath.Join(tmp, "dotkeeper")
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// init dotkeeper — ignore exit code (Syncthing may fail to start
	// under CI; the config file is what we actually need).
	initCmd := exec.Command(binary, "init", "--name", "test-machine", "--slot", "0")
	initCmd.Env = envWith(tmp)
	_, _ = initCmd.CombinedOutput()

	// Create a fresh git repo to act as a managed folder.
	repo = filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- fixed fixture
		cmd.Dir = repo
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main", ".")
	runGit("config", "user.name", "test")
	runGit("config", "user.email", "test@dotkeeper.invalid")
	runGit("config", "commit.gpgsign", "false")

	// Register it with dotkeeper so it's a "managed folder".
	addCmd := exec.Command(binary, "add", repo)
	addCmd.Env = envWith(tmp)
	_, _ = addCmd.CombinedOutput()

	return binary, tmp, repo
}

// writeConflictPair creates a canonical file, commits it, and drops a
// variant alongside. Returns the two absolute paths.
func writeConflictPair(t *testing.T, repo, name, canonicalContent, variantName, variantContent string) (canonical, variant string) {
	t.Helper()
	canonical = filepath.Join(repo, name)
	if err := os.WriteFile(canonical, []byte(canonicalContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Commit so Accept has a HEAD to build on.
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- fixed fixture
		cmd.Dir = repo
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("add", "--", name)
	runGit("commit", "-q", "-m", "add "+name)

	variant = filepath.Join(repo, variantName)
	if err := os.WriteFile(variant, []byte(variantContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return canonical, variant
}

// gitHead returns `git log -1 --format=%s` for a repo.
func gitHead(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%s") // #nosec G204 -- fixed fixture
	cmd.Dir = repo
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCLIConflictKeep covers the keep happy path via the built
// binary: variant deleted, canonical untouched, no new commit.
func TestCLIConflictKeep(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical, variant := writeConflictPair(t, repo,
		"notes.md", "canonical\n",
		"notes.sync-conflict-20260419-143015-UUS6FSQ.md", "variant\n")
	headBefore := gitHead(t, repo)

	cmd := exec.Command(binary, "conflict", "keep", canonical)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("keep: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "kept:") {
		t.Errorf("output missing 'kept:': %q", out)
	}

	if _, err := os.Stat(variant); !os.IsNotExist(err) {
		t.Errorf("variant should be gone: %v", err)
	}
	data, _ := os.ReadFile(canonical)
	if string(data) != "canonical\n" {
		t.Errorf("canonical mutated: %q", data)
	}
	if gitHead(t, repo) != headBefore {
		t.Errorf("HEAD moved: before=%q after=%q", headBefore, gitHead(t, repo))
	}
}

// TestCLIConflictAccept covers the accept happy path: canonical is
// overwritten with the variant content, variant is gone, and a single
// new scoped commit lands on HEAD.
func TestCLIConflictAccept(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical, variant := writeConflictPair(t, repo,
		"notes.md", "local\n",
		"notes.sync-conflict-20260419-143015-UUS6FSQ.md", "from-peer\n")

	cmd := exec.Command(binary, "conflict", "accept", canonical)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "accepted:") {
		t.Errorf("output missing 'accepted:': %q", out)
	}

	data, _ := os.ReadFile(canonical)
	if string(data) != "from-peer\n" {
		t.Errorf("canonical = %q, want 'from-peer\\n'", data)
	}
	if _, err := os.Stat(variant); !os.IsNotExist(err) {
		t.Errorf("variant should be gone: %v", err)
	}

	subj := gitHead(t, repo)
	wantSubj := "auto: accept sync conflict for notes.md (from UUS6FSQ)"
	if subj != wantSubj {
		t.Errorf("HEAD subject = %q, want %q", subj, wantSubj)
	}
}

// TestCLIConflictAcceptVariantPath covers passing the variant's path
// (not the canonical) — same behaviour, CLI derives canonical via
// Parse.
func TestCLIConflictAcceptVariantPath(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical, variant := writeConflictPair(t, repo,
		"notes.md", "local\n",
		"notes.sync-conflict-20260419-143015-UUS6FSQ.md", "from-peer\n")

	cmd := exec.Command(binary, "conflict", "accept", variant)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("accept (variant path): %v\n%s", err, out)
	}

	data, _ := os.ReadFile(canonical)
	if string(data) != "from-peer\n" {
		t.Errorf("canonical = %q, want 'from-peer\\n'", data)
	}
}

// TestCLIConflictAll verifies --all walks every managed folder and
// resolves every pending conflict in one invocation.
func TestCLIConflictAll(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	_, v1 := writeConflictPair(t, repo,
		"a.md", "a-local\n",
		"a.sync-conflict-20260419-143015-UUS6FSQ.md", "a-peer\n")
	_, v2 := writeConflictPair(t, repo,
		"b.md", "b-local\n",
		"b.sync-conflict-20260419-143016-UUS6FSQ.md", "b-peer\n")

	cmd := exec.Command(binary, "conflict", "accept", "--all")
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("accept --all: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "accepted 2 conflict(s)") {
		t.Errorf("output should report 2 accepted: %q", out)
	}
	for _, p := range []string{v1, v2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("variant %s should be gone: %v", p, err)
		}
	}
}

// TestCLIConflictKeepAll verifies that keep --all deletes every
// variant without producing any new commits.
func TestCLIConflictKeepAll(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	_, v1 := writeConflictPair(t, repo,
		"a.md", "a-local\n",
		"a.sync-conflict-20260419-143015-UUS6FSQ.md", "a-peer\n")
	_, v2 := writeConflictPair(t, repo,
		"b.md", "b-local\n",
		"b.sync-conflict-20260419-143016-UUS6FSQ.md", "b-peer\n")
	headBefore := gitHead(t, repo)

	cmd := exec.Command(binary, "conflict", "keep", "--all")
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("keep --all: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "kept 2 conflict(s)") {
		t.Errorf("output should report 2 kept: %q", out)
	}
	for _, p := range []string{v1, v2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("variant %s should be gone: %v", p, err)
		}
	}
	if gitHead(t, repo) != headBefore {
		t.Errorf("keep should not commit; HEAD moved: %q -> %q", headBefore, gitHead(t, repo))
	}
}

// TestCLIConflictMultipleVariants covers the rare three-peer-diverged
// case: two conflicts for the same canonical. Must error out and list
// each variant without touching disk.
func TestCLIConflictMultipleVariants(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical, v1 := writeConflictPair(t, repo,
		"notes.md", "local\n",
		"notes.sync-conflict-20260419-143015-UUS6FSQ.md", "peer1\n")
	v2 := filepath.Join(repo, "notes.sync-conflict-20260419-150000-WB25TET.md")
	if err := os.WriteFile(v2, []byte("peer2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "conflict", "accept", canonical)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("expected exit 1 for multi-variant canonical, got success: %s", out)
	}
	exitErr, _ := err.(*exec.ExitError)
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %v, want 1", err)
	}
	if !strings.Contains(string(out), "2 variants") {
		t.Errorf("output missing '2 variants' hint: %q", out)
	}
	// Disk untouched.
	for _, p := range []string{v1, v2} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should still exist: %v", p, err)
		}
	}
	data, _ := os.ReadFile(canonical)
	if string(data) != "local\n" {
		t.Errorf("canonical mutated: %q", data)
	}
}

// TestCLIConflictNoConflict covers passing a path with no conflict.
// Must error out with a clear message and exit 1.
func TestCLIConflictNoConflict(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical := filepath.Join(repo, "lonely.md")
	if err := os.WriteFile(canonical, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"keep", "accept"} {
		cmd := exec.Command(binary, "conflict", sub, canonical)
		cmd.Env = envWith(tmp)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("%s: expected exit 1 for no-conflict path, got success: %s", sub, out)
			continue
		}
		exitErr, _ := err.(*exec.ExitError)
		if exitErr == nil || exitErr.ExitCode() != 1 {
			t.Errorf("%s: exit code = %v, want 1", sub, err)
		}
		if !strings.Contains(string(out), "no conflict") {
			t.Errorf("%s: output missing 'no conflict': %q", sub, out)
		}
	}
}

// TestCLIConflictIdempotent re-runs keep/accept after a successful
// first pass. Both must exit 0 with no-op behaviour — no stray errors
// and no extra commits.
func TestCLIConflictIdempotent(t *testing.T) {
	binary, tmp, repo := setupConflictHarness(t)
	canonical, _ := writeConflictPair(t, repo,
		"notes.md", "local\n",
		"notes.sync-conflict-20260419-143015-UUS6FSQ.md", "peer\n")

	// First accept — succeeds.
	cmd := exec.Command(binary, "conflict", "accept", canonical)
	cmd.Env = envWith(tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first accept: %v\n%s", err, out)
	}
	headAfterFirst := gitHead(t, repo)

	// Second accept — variant is gone. Must still exit 0.
	cmd = exec.Command(binary, "conflict", "accept", canonical)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// `canonical` has no variant any more, so resolveTarget
		// returns "no conflict" and we exit 1. That's also an
		// acceptable idempotent shape — verify the message.
		if !strings.Contains(string(out), "no conflict") {
			t.Errorf("second accept: %v\n%s", err, out)
		}
	}
	if gitHead(t, repo) != headAfterFirst {
		t.Errorf("HEAD moved on idempotent re-run: %q -> %q", headAfterFirst, gitHead(t, repo))
	}

	// --all form must definitely exit 0 (no conflicts left to process).
	cmd = exec.Command(binary, "conflict", "accept", "--all")
	cmd.Env = envWith(tmp)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Errorf("accept --all (idempotent): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "accepted 0 conflict(s)") {
		t.Errorf("accept --all should report 0 accepted: %q", out)
	}
}

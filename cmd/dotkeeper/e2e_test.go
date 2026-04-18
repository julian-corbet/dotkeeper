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

// TestE2EInitStatus tests that init creates config and status reads it.
func TestE2EInitStatus(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	// Build the binary
	binary := filepath.Join(tmp, "dotkeeper")
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Init
	cmd := exec.Command(binary, "init", "--name", "test-machine", "--slot", "0")
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	output := string(out)

	if !strings.Contains(output, "test-machine") {
		t.Errorf("init output missing machine name:\n%s", output)
	}
	if !strings.Contains(output, "device ID") {
		t.Errorf("init output missing device ID:\n%s", output)
	}

	// Verify machine.toml was created
	machineToml := filepath.Join(tmp, "config", "dotkeeper", "machine.toml")
	if _, err := os.Stat(machineToml); os.IsNotExist(err) {
		t.Error("machine.toml not created")
	}

	// Verify config.toml was created
	configToml := filepath.Join(tmp, "config", "dotkeeper", "config.toml")
	if _, err := os.Stat(configToml); os.IsNotExist(err) {
		t.Error("config.toml not created")
	}

	// Status (without Syncthing running — should still show machine info)
	cmd = exec.Command(binary, "status")
	cmd.Env = envWith(tmp)
	out, _ = cmd.CombinedOutput()
	output = string(out)

	if !strings.Contains(output, "test-machine") {
		t.Errorf("status output missing machine name:\n%s", output)
	}
	if !strings.Contains(output, "Slot: 0") {
		t.Errorf("status output missing slot:\n%s", output)
	}
}

// TestE2EAddRepo tests that add creates the per-repo dotkeeper.toml and updates config.
func TestE2EAddRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	binary := filepath.Join(tmp, "dotkeeper")
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Init first
	cmd := exec.Command(binary, "init", "--name", "test-machine", "--slot", "0")
	cmd.Env = envWith(tmp)
	_, _ = cmd.CombinedOutput()

	// Create a fake repo to add
	repoDir := filepath.Join(tmp, "my-project")
	_ = os.MkdirAll(repoDir, 0o755)

	// Init it as a git repo
	gitInit := exec.Command("git", "init", repoDir)
	gitInit.Env = gitEnv()
	_, _ = gitInit.CombinedOutput()

	// Add it — may fail due to Syncthing not running for folder config; that's OK.
	// The config update and repo log should still happen.
	cmd = exec.Command(binary, "add", repoDir)
	cmd.Env = envWith(tmp)
	out, _ := cmd.CombinedOutput()
	output := string(out)

	if !strings.Contains(output, "added: my-project") {
		t.Errorf("add output unexpected:\n%s", output)
	}

	// Verify dotkeeper.toml was created in the repo
	repoLog := filepath.Join(repoDir, "dotkeeper.toml")
	if _, err := os.Stat(repoLog); os.IsNotExist(err) {
		t.Error("dotkeeper.toml not created in repo")
	}

	// Verify .stignore was created
	stignore := filepath.Join(repoDir, ".stignore")
	if _, err := os.Stat(stignore); os.IsNotExist(err) {
		t.Error(".stignore not created in repo")
	}

	// Verify config.toml was updated
	configData, _ := os.ReadFile(filepath.Join(tmp, "config", "dotkeeper", "config.toml"))
	if !strings.Contains(string(configData), "my-project") {
		t.Error("config.toml not updated with new repo")
	}
}

// TestE2ESyncGitRepo tests the full git sync cycle.
func TestE2ESyncGitRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	binary := filepath.Join(tmp, "dotkeeper")
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Create a git repo with remote
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(tmp, "init", "--bare", bare)
	runGit(tmp, "clone", bare, work)
	runGit(work, "checkout", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# test\n"), 0o644)
	runGit(work, "add", ".")
	runGit(work, "commit", "-m", "initial")
	runGit(work, "push", "-u", "origin", "main")

	// Init dotkeeper
	cmd := exec.Command(binary, "init", "--name", "test-machine", "--slot", "0")
	cmd.Env = envWith(tmp)
	_, _ = cmd.CombinedOutput()

	// Add the repo
	cmd = exec.Command(binary, "add", work)
	cmd.Env = envWith(tmp)
	_, _ = cmd.CombinedOutput()

	// Create a change
	_ = os.WriteFile(filepath.Join(work, "new-file.txt"), []byte("synced\n"), 0o644)

	// Run sync
	cmd = exec.Command(binary, "sync")
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sync failed: %v\n%s", err, out)
	}
	output := string(out)

	if !strings.Contains(output, "committed changes") {
		t.Errorf("sync didn't commit:\n%s", output)
	}
	if !strings.Contains(output, "pushed") {
		t.Errorf("sync didn't push:\n%s", output)
	}

	// Verify the file was pushed to remote
	clone2 := filepath.Join(tmp, "clone2")
	runGit(tmp, "clone", "-b", "main", bare, clone2)
	if _, err := os.Stat(filepath.Join(clone2, "new-file.txt")); os.IsNotExist(err) {
		// Check what's in the clone
		ls, _ := exec.Command("ls", "-la", clone2).Output()
		gitLog := exec.Command("git", "log", "--oneline", "--all")
		gitLog.Dir = clone2
		logOut, _ := gitLog.Output()
		t.Errorf("new-file.txt not found in fresh clone — push didn't work\nls: %s\ngit log: %s", ls, logOut)
	}
}

// --- Helpers ---

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func envWith(tmp string) []string {
	return append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(tmp, "config"),
		"XDG_DATA_HOME="+filepath.Join(tmp, "data"),
		"HOME="+tmp,
		// Git identity for CI environments where HOME override
		// loses the global gitconfig
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
}

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

// TestE2EInitStatus tests that init creates machine.toml and status reads it.
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

// TestE2EReconcileWithTrackedRepo tests that 'dotkeeper reconcile' succeeds
// on a machine initialized with a tracked repo override in state.toml.
func TestE2EReconcileWithTrackedRepo(t *testing.T) {
	tmp := t.TempDir()

	binary := filepath.Join(tmp, "dotkeeper")
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Write a v2 machine.toml with empty scan roots.
	writeMinimalMachineV2(t, tmp, "test-machine")

	// Create a git repo to track.
	repoDir := filepath.Join(tmp, "my-repo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)

	// Track it via dotkeeper track.
	cmd := exec.Command(binary, "track", repoDir)
	cmd.Env = envWith(tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("track failed: %v\n%s", err, out)
	}

	// Reconcile should succeed (empty plan since no dotkeeper.toml in repo yet).
	cmd = exec.Command(binary, "reconcile")
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reconcile failed: %v\n%s", err, out)
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

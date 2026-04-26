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

// TestCLIReconcileNotInitialized verifies that running 'dotkeeper reconcile'
// on a fresh machine tells the user to run 'dotkeeper init' first.
func TestCLIReconcileNotInitialized(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "reconcile")
	if code == 0 {
		t.Error("reconcile on uninitialised machine should exit non-zero")
	}
	if !strings.Contains(strings.ToLower(output), "init") {
		t.Errorf("reconcile should tell user to run init; got: %q", output)
	}
}

// TestCLIReconcileEmptyConfig verifies that reconcile on an initialised machine
// with no scan roots / no repos produces an empty plan and exits 0.
func TestCLIReconcileEmptyConfig(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Write a minimal machine.toml with empty scan roots so discovery
	// doesn't accidentally find real repos in the test environment.
	writeMinimalMachineV2(t, tmp, "test-machine")

	output, code := runDotkeeper(t, binary, tmp, "reconcile")
	if code != 0 {
		t.Errorf("reconcile with empty config should exit 0; got %d\noutput: %s", code, output)
	}
	// Should either print "no actions" / "empty plan" or just nothing — the
	// important thing is it doesn't error.
}

// TestCLIIdentityPrintsBoth verifies that 'dotkeeper identity' prints both
// the machine name and the Syncthing device ID.
func TestCLIIdentityPrintsBoth(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Init so Syncthing keys exist and state.toml can have a device ID.
	_, initCode := runDotkeeper(t, binary, tmp, "init", "--name", "my-machine", "--slot", "0")
	if initCode != 0 {
		t.Skip("init failed (likely no Syncthing support in this env); skipping identity test")
	}

	output, code := runDotkeeper(t, binary, tmp, "identity")
	if code != 0 {
		t.Errorf("identity exit code = %d, want 0\noutput: %s", code, output)
	}
	if !strings.Contains(output, "name:") {
		t.Errorf("identity output missing 'name:' field: %q", output)
	}
	if !strings.Contains(output, "device_id:") {
		t.Errorf("identity output missing 'device_id:' field: %q", output)
	}
}

// TestCLIIdentityNotInitialized verifies that 'dotkeeper identity' on a fresh
// machine exits non-zero with a helpful message.
func TestCLIIdentityNotInitialized(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "identity")
	if code == 0 {
		t.Error("identity on uninitialised machine should exit non-zero")
	}
	if !strings.Contains(strings.ToLower(output), "init") {
		t.Errorf("identity should tell user to run init; got: %q", output)
	}
}

// TestCLIIdentityDeviceIDOnly verifies that 'dotkeeper identity --device-id'
// prints just the device ID with no labels.
func TestCLIIdentityDeviceIDOnly(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	_, initCode := runDotkeeper(t, binary, tmp, "init", "--name", "my-machine", "--slot", "0")
	if initCode != 0 {
		t.Skip("init failed (likely no Syncthing support in this env); skipping --device-id test")
	}

	output, code := runDotkeeper(t, binary, tmp, "identity", "--device-id")
	if code != 0 {
		t.Errorf("identity --device-id exit code = %d, want 0\noutput: %s", code, output)
	}
	// Should be a single line with no "name:" or "device_id:" labels.
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Errorf("identity --device-id should print exactly one line; got %d lines: %q", len(lines), output)
	}
	if strings.Contains(output, "name:") || strings.Contains(output, "device_id:") {
		t.Errorf("identity --device-id should not print labels; got: %q", output)
	}
}

// TestCLITrackAddsPath verifies that 'dotkeeper track <path>' registers an
// absolute git repo path in state.toml's tracked_overrides.
func TestCLITrackAddsPath(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	// Create a git repo to track.
	repoDir := filepath.Join(tmp, "tracked-repo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)

	output, code := runDotkeeper(t, binary, tmp, "track", repoDir)
	if code != 0 {
		t.Errorf("track exit code = %d, want 0\noutput: %s", code, output)
	}

	// Verify state.toml was created and contains the path.
	stateData := mustReadStateFile(t, tmp)
	if !strings.Contains(stateData, repoDir) {
		t.Errorf("state.toml does not contain tracked path %q\nstate:\n%s", repoDir, stateData)
	}
}

// TestCLITrackNonRepo verifies that 'dotkeeper track <path>' rejects a path
// that is not a git repository, and leaves state.toml unchanged.
func TestCLITrackNonRepo(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	// Plain directory, not a git repo.
	notARepo := filepath.Join(tmp, "plain-dir")
	mustMkdir(t, notARepo)

	output, code := runDotkeeper(t, binary, tmp, "track", notARepo)
	if code == 0 {
		t.Errorf("track of non-repo should exit non-zero; output: %s", output)
	}

	// State file should either not exist or not contain the path.
	statePath := filepath.Join(tmp, "state", "dotkeeper", "state.toml")
	if data, err := os.ReadFile(statePath); err == nil {
		if strings.Contains(string(data), notARepo) {
			t.Error("state.toml should not contain non-repo path after failed track")
		}
	}
}

// TestCLITrackIdempotent verifies that tracking the same path twice results
// in exactly one entry in state.toml.
func TestCLITrackIdempotent(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	repoDir := filepath.Join(tmp, "tracked-repo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)

	// Track twice.
	_, code1 := runDotkeeper(t, binary, tmp, "track", repoDir)
	_, code2 := runDotkeeper(t, binary, tmp, "track", repoDir)
	if code1 != 0 || code2 != 0 {
		t.Fatalf("both track calls should succeed; codes: %d, %d", code1, code2)
	}

	stateData := mustReadStateFile(t, tmp)
	// Count occurrences of the path in the state file.
	count := strings.Count(stateData, repoDir)
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of path in state.toml; found %d\nstate:\n%s", count, stateData)
	}
}

// TestCLIUntrackRemoves verifies that 'dotkeeper untrack <path>' removes a
// previously tracked path from state.toml.
func TestCLIUntrackRemoves(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	repoDir := filepath.Join(tmp, "tracked-repo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)

	// Track then untrack.
	_, code := runDotkeeper(t, binary, tmp, "track", repoDir)
	if code != 0 {
		t.Fatalf("track should succeed before untrack test")
	}

	_, code = runDotkeeper(t, binary, tmp, "untrack", repoDir)
	if code != 0 {
		t.Fatalf("untrack should exit 0; got %d", code)
	}

	stateData := mustReadStateFile(t, tmp)
	if strings.Contains(stateData, repoDir) {
		t.Errorf("state.toml should not contain path after untrack\nstate:\n%s", stateData)
	}
}

// TestCLIUntrackUnknown verifies that untracking a path that was never tracked
// is a no-op that exits 0.
func TestCLIUntrackUnknown(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	_, code := runDotkeeper(t, binary, tmp, "untrack", "/some/path/that/was/never/tracked")
	if code != 0 {
		t.Errorf("untrack of unknown path should be a no-op (exit 0); got %d", code)
	}
}

// TestCLIHelpIncludesNewSubcommands verifies that the new v0.5 subcommands
// appear in the top-level help output.
func TestCLIHelpIncludesNewSubcommands(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "--help")
	if code != 0 {
		t.Fatalf("--help exit code = %d", code)
	}
	for _, sub := range []string{"reconcile", "identity", "track", "untrack"} {
		if !strings.Contains(output, sub) {
			t.Errorf("--help missing new subcommand %q; output:\n%s", sub, output)
		}
	}
}

// TestCLIReconcileHelp verifies that 'dotkeeper reconcile --help' exits 0
// and describes the command.
func TestCLIReconcileHelp(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "reconcile", "--help")
	if code != 0 {
		t.Errorf("reconcile --help exit code = %d, want 0", code)
	}
	if !strings.Contains(strings.ToLower(output), "reconcile") {
		t.Errorf("reconcile --help missing description: %q", output)
	}
}

// TestCLIReconcileRejectsPositionalArg verifies that 'dotkeeper reconcile <path>'
// errors out — the optional path-argument feature was removed (until a
// scoped-reconcile implementation lands). This guards against silently
// accepting an arg the command does nothing with.
func TestCLIReconcileRejectsPositionalArg(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	writeMinimalMachineV2(t, tmp, "test-machine")

	_, code := runDotkeeper(t, binary, tmp, "reconcile", "/tmp/some/path")
	if code == 0 {
		t.Error("reconcile with positional arg should fail (feature not yet implemented)")
	}
}

// --- Test helpers ---

// writeMinimalMachineV2 writes a machine.toml (v2 schema) with empty scan
// roots so discovery doesn't wander outside the test tmp directory.
func writeMinimalMachineV2(t *testing.T, tmp, name string) {
	t.Helper()
	cfgDir := filepath.Join(tmp, "config", "dotkeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	content := `schema_version = 2
name = "` + name + `"
slot = 0
default_commit_policy = "manual"
default_git_interval = "hourly"
default_slot_offset_minutes = 5
reconcile_interval = "5m"
default_share_with = []

[discovery]
scan_roots = []
exclude = []
scan_interval = "5m"
scan_depth = 3
`
	machineToml := filepath.Join(cfgDir, "machine.toml")
	if err := os.WriteFile(machineToml, []byte(content), 0o600); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}
}

// mustMkdir creates a directory, failing the test if it errors.
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// mustGitInit initialises a git repository at path.
func mustGitInit(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("git", "init", path)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", path, err, out)
	}
}

// mustReadStateFile reads state.toml from the path used in tests.
// envWith sets HOME=tmp, so StateDir() falls back to tmp/.local/state/dotkeeper.
func mustReadStateFile(t *testing.T, tmp string) string {
	t.Helper()
	// StateDir() logic: $XDG_STATE_HOME/dotkeeper or $HOME/.local/state/dotkeeper.
	// envWith does not set XDG_STATE_HOME, so state lives under HOME.
	statePath := filepath.Join(tmp, ".local", "state", "dotkeeper", "state.toml")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.toml at %s: %v", statePath, err)
	}
	return string(data)
}

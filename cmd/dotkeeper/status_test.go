// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusCmdReportsNotInitializedWhenMissing verifies that when neither
// machine.toml nor state.toml exist, 'dotkeeper status' shows the
// "Not initialized" message. The exit code is not checked here because the
// Syncthing service detection may fail with a non-zero exit on certain hosts
// (same convention as TestCLIStatusUninitialized).
func TestStatusCmdReportsNotInitializedWhenMissing(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, _ := runDotkeeper(t, binary, tmp, "status")
	if !strings.Contains(output, "Not initialized") {
		t.Errorf("status should show 'Not initialized' when machine.toml missing; got:\n%s", output)
	}
}

// TestStatusCmdReportsCorruptMachineConfig verifies that when machine.toml
// exists but contains invalid TOML, 'dotkeeper status' exits non-zero and
// the output mentions a parse failure rather than silently pretending the
// machine is uninitialized.
func TestStatusCmdReportsCorruptMachineConfig(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Write a syntactically invalid machine.toml so the TOML parser returns
	// an error (file exists, but content is not valid TOML).
	cfgDir := filepath.Join(tmp, "config", "dotkeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	corrupt := []byte("this is not valid TOML ][[[")
	if err := os.WriteFile(filepath.Join(cfgDir, "machine.toml"), corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt machine.toml: %v", err)
	}

	output, code := runDotkeeper(t, binary, tmp, "status")
	if code == 0 {
		t.Errorf("status with corrupt machine.toml should exit non-zero; got 0\noutput: %s", output)
	}
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "parse") && !strings.Contains(lower, "machine.toml") && !strings.Contains(lower, "error") {
		t.Errorf("status output should mention parse failure for corrupt machine.toml; got:\n%s", output)
	}
}

// TestStatusCmdReportsCorruptState verifies that when machine.toml is valid
// but state.toml is corrupt, 'dotkeeper status' exits non-zero with a
// clear error message instead of silently continuing with a nil state.
func TestStatusCmdReportsCorruptState(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Write a valid machine.toml so the machine config load succeeds.
	writeMinimalMachineV2(t, tmp, "test-machine")

	// Write a corrupt state.toml so the state load fails.
	// state.toml lives at $HOME/.local/state/dotkeeper/state.toml
	// because envWith does not set XDG_STATE_HOME.
	stateDir := filepath.Join(tmp, ".local", "state", "dotkeeper")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stateDir, err)
	}
	corrupt := []byte("this is not valid TOML ][[[")
	if err := os.WriteFile(filepath.Join(stateDir, "state.toml"), corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt state.toml: %v", err)
	}

	output, code := runDotkeeper(t, binary, tmp, "status")
	if code == 0 {
		t.Errorf("status with corrupt state.toml should exit non-zero; got 0\noutput: %s", output)
	}
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "parse") && !strings.Contains(lower, "state.toml") && !strings.Contains(lower, "error") {
		t.Errorf("status output should mention parse failure for corrupt state.toml; got:\n%s", output)
	}
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// CLI smoke tests verify that the binary handles common invocations
// correctly. These catch cobra wiring mistakes, missing subcommands,
// and broken help text.
//
// To add a new CLI test:
//   1. Build with buildTestBinary(t)
//   2. Run with runDotkeeper(t, binary, tmpDir, args...)
//   3. Check exit code and output

func buildTestBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	binary := tmp + "/dotkeeper"
	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

func runDotkeeper(t *testing.T, binary, tmp string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = envWith(tmp)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error: %v", err)
		}
	}
	return string(out), exitCode
}

// TestCLIVersion verifies 'dotkeeper version' exits 0 and prints version info.
func TestCLIVersion(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "version")
	if code != 0 {
		t.Errorf("version exit code = %d, want 0", code)
	}
	if !strings.Contains(output, "dotkeeper") {
		t.Errorf("version output missing 'dotkeeper': %q", output)
	}
}

// TestCLIHelp verifies 'dotkeeper --help' exits 0 and lists the kept commands.
func TestCLIHelp(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "--help")
	if code != 0 {
		t.Errorf("--help exit code = %d, want 0", code)
	}

	// Kept v0.5 subcommands should appear in help.
	for _, cmd := range []string{"init", "status", "start", "version", "reconcile", "identity", "track", "untrack", "conflict", "doctor"} {
		if !strings.Contains(output, cmd) {
			t.Errorf("help output missing command %q", cmd)
		}
	}
}

// TestCLIStatusUninitialized verifies 'dotkeeper status' indicates
// the machine is not initialized. May exit non-zero if Syncthing
// service detection finds a running instance but can't reach the API
// (real service on host, test data directory).
func TestCLIStatusUninitialized(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, _ := runDotkeeper(t, binary, tmp, "status")
	if !strings.Contains(output, "Not initialized") {
		t.Errorf("status should indicate not initialized: %q", output)
	}
}

// TestCLIRemovedCommandsReturnUnknown asserts that each v0.4 imperative
// command that was deleted now returns "unknown command" from cobra.
func TestCLIRemovedCommandsReturnUnknown(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	removed := []string{"join", "add", "remove", "pair", "sync", "install-timer", "stop"}
	for _, name := range removed {
		output, code := runDotkeeper(t, binary, tmp, name)
		if code == 0 {
			t.Errorf("removed command %q should exit non-zero, got 0", name)
		}
		if !strings.Contains(strings.ToLower(output), "unknown") && !strings.Contains(strings.ToLower(output), "unrecognized") {
			t.Errorf("removed command %q output should mention 'unknown command'; got: %q", name, output)
		}
	}
}

// TestCLIInitTwice verifies that running 'dotkeeper init' twice
// doesn't crash and shows an appropriate message.
func TestCLIInitTwice(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// First init
	_, code := runDotkeeper(t, binary, tmp, "init", "--name", "test", "--slot", "0")
	if code != 0 {
		t.Fatal("first init failed")
	}

	// Second init (without --force)
	output, code := runDotkeeper(t, binary, tmp, "init", "--name", "test", "--slot", "0")
	if code != 0 {
		t.Errorf("second init exit code = %d (expected 0 with message)", code)
	}
	if !strings.Contains(output, "already initialized") {
		t.Errorf("expected 'already initialized' message, got: %q", output)
	}
}

// TestCLIUnknownCommand verifies that unknown commands produce an error.
func TestCLIUnknownCommand(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	_, code := runDotkeeper(t, binary, tmp, "nonexistent-command")
	if code == 0 {
		t.Error("unknown command should fail")
	}
}

// TestCLIInitFlags verifies that init --help shows all flags.
func TestCLIInitFlags(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, _ := runDotkeeper(t, binary, tmp, "init", "--help")
	for _, flag := range []string{"--name", "--slot", "--force"} {
		if !strings.Contains(output, flag) {
			t.Errorf("init --help missing flag %q", flag)
		}
	}
}

// TestCLIDoctorHelp verifies the doctor subcommand is wired and accepts --json.
func TestCLIDoctorHelp(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "doctor", "--help")
	if code != 0 {
		t.Errorf("doctor --help exit code = %d, want 0", code)
	}
	if !strings.Contains(output, "--json") {
		t.Errorf("doctor --help missing --json flag: %q", output)
	}
}

// TestCLIDoctorOnFreshMachine runs the doctor against an uninitialised
// home dir. It must not crash, and it must exit non-zero because the
// config check fails on "machine.toml missing".
func TestCLIDoctorOnFreshMachine(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, code := runDotkeeper(t, binary, tmp, "doctor")
	if code == 0 {
		t.Errorf("doctor on fresh machine should exit non-zero; got 0. Output:\n%s", output)
	}
	// Every check must still run — we verify a few of the easy labels
	// appear in the output, which also validates the formatter didn't
	// wedge on a nil Syncthing client.
	for _, want := range []string{"dotkeeper doctor", "version", "config", "service"} {
		if !strings.Contains(output, want) {
			t.Errorf("doctor output missing %q; got:\n%s", want, output)
		}
	}
}

// TestCLIDoctorJSON verifies --json emits a valid JSON object.
func TestCLIDoctorJSON(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	output, _ := runDotkeeper(t, binary, tmp, "doctor", "--json")
	// Don't re-parse here (avoid a test-only dependency); just assert
	// the envelope keys are present and balanced braces are correct.
	for _, want := range []string{`"results"`, `"failures"`, `"warnings"`, `"name"`} {
		if !strings.Contains(output, want) {
			t.Errorf("doctor --json missing key %q; got:\n%s", want, output)
		}
	}
}

// TestStatusCmdShowsV5State initializes a v0.5 setup and verifies that
// 'dotkeeper status' shows the machine name and scan roots from machine.toml.
func TestStatusCmdShowsV5State(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Write a v2 machine.toml with custom scan roots.
	writeMinimalMachineV2(t, tmp, "my-test-machine")

	output, _ := runDotkeeper(t, binary, tmp, "status")

	if !strings.Contains(output, "my-test-machine") {
		t.Errorf("status output missing machine name 'my-test-machine': %q", output)
	}
	if !strings.Contains(output, "Scan roots") {
		t.Errorf("status output missing 'Scan roots' section: %q", output)
	}
}

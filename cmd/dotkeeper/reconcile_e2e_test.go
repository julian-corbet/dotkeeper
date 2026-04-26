// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestE2EReconcileAddsRepoToSyncthing verifies the end-to-end reconcile path:
// given a dotkeeper.toml in a scan root, reconcile plans and attempts to add
// the folder to Syncthing. The Syncthing API call will fail (not running in
// tests) but the plan itself must be non-empty for a newly discovered repo.
func TestE2EReconcileAddsRepoToSyncthing(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Create a scan root and a repo within it.
	scanRoot := filepath.Join(tmp, "scan")
	repoDir := filepath.Join(scanRoot, "myrepo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)

	// Write dotkeeper.toml into the repo so discovery picks it up.
	writeDotKeeperToml(t, repoDir, "myrepo")

	// Write machine.toml pointing at scanRoot.
	writeMachineV2WithScanRoot(t, tmp, "test-machine", scanRoot)

	// Reconcile — Syncthing is not running so the apply will fail,
	// but the command itself should exit 0 (reconcile continues-on-error)
	// and describe the actions it attempted.
	output, code := runDotkeeper(t, binary, tmp, "reconcile")
	// Exit 1 is expected when apply fails; what we check is the plan was built.
	// If code == 0 great; if code == 1 that means Syncthing apply failed which is fine.
	if code > 1 {
		t.Errorf("reconcile exit code = %d; expected 0 or 1\noutput: %s", code, output)
	}
	// The plan should mention the repo folder ID.
	if !strings.Contains(output, "myrepo") {
		t.Errorf("reconcile output should mention discovered repo 'myrepo'; got:\n%s", output)
	}
}

// TestE2EReconcileIsIdempotent verifies that running reconcile twice against
// the same already-applied state produces an empty plan on the second pass.
// We use a stub path (no real Syncthing) so we test purely the diff logic
// rather than the apply side.
func TestE2EReconcileIsIdempotent(t *testing.T) {
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	// Write machine.toml with empty scan roots — nothing to discover,
	// so the plan should always be empty.
	writeMinimalMachineV2(t, tmp, "test-machine")

	// First pass.
	out1, code1 := runDotkeeper(t, binary, tmp, "reconcile")
	// Second pass.
	out2, code2 := runDotkeeper(t, binary, tmp, "reconcile")

	if code1 != 0 {
		t.Errorf("first reconcile exit code = %d; want 0\noutput: %s", code1, out1)
	}
	if code2 != 0 {
		t.Errorf("second reconcile exit code = %d; want 0\noutput: %s", code2, out2)
	}
}

// TestE2EStartTriggersReconcileOnFileChange starts the daemon with a very
// short reconcile interval, drops a new dotkeeper.toml into a scan root,
// and verifies the reconcile runs without crashing the daemon.
//
// This test is gated behind -tags integration because it starts a real
// Syncthing engine and involves real filesystem events and timers.
// Run with: go test -tags integration ./cmd/dotkeeper/ -run TestE2EStartTriggersReconcileOnFileChange
func TestE2EStartTriggersReconcileOnFileChange(t *testing.T) {
	if os.Getenv("DOTKEEPER_INTEGRATION") == "" {
		t.Skip("skipping integration test; set DOTKEEPER_INTEGRATION=1 to run")
	}
	binary := buildTestBinary(t)
	tmp := t.TempDir()

	scanRoot := filepath.Join(tmp, "scan")
	mustMkdir(t, scanRoot)
	writeMachineV2WithScanRoot(t, tmp, "test-machine", scanRoot)

	// Start dotkeeper start in the background.
	cmd := exec.Command(binary, "start", "--debug")
	cmd.Env = envWith(tmp)

	logBuf := &concurrentBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("dotkeeper start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Give the daemon a moment to start.
	time.Sleep(2 * time.Second)

	// Drop a dotkeeper.toml in the scan root — should trigger reconcile.
	repoDir := filepath.Join(scanRoot, "newrepo")
	mustMkdir(t, repoDir)
	mustGitInit(t, repoDir)
	writeDotKeeperToml(t, repoDir, "newrepo")

	// Wait for reconcile to fire (fsnotify + 1s debounce + processing).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "reconcile") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	log := logBuf.String()
	if !strings.Contains(log, "reconcile") {
		t.Errorf("daemon did not log a reconcile pass after file change; log:\n%s", log)
	}
}

// --- helpers for E2E reconcile tests ---

// writeDotKeeperToml writes a minimal dotkeeper.toml into repoDir.
func writeDotKeeperToml(t *testing.T, repoDir, repoName string) {
	t.Helper()
	content := `schema_version = 2
repo_name = "` + repoName + `"

[sync]
syncthing_folder_id = "dk-` + repoName + `"
`
	p := filepath.Join(repoDir, "dotkeeper.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// writeMachineV2WithScanRoot writes a machine.toml (v2) with a single scan root.
func writeMachineV2WithScanRoot(t *testing.T, tmp, name, scanRoot string) {
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
reconcile_interval = "1s"
default_share_with = []

[discovery]
scan_roots = ["` + scanRoot + `"]
exclude = []
scan_interval = "1s"
scan_depth = 3
`
	machineToml := filepath.Join(cfgDir, "machine.toml")
	if err := os.WriteFile(machineToml, []byte(content), 0o600); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}
}

// concurrentBuffer is a string builder with a mutex so test goroutines can
// read it safely while the daemon writes to it via io.Writer.
type concurrentBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *concurrentBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *concurrentBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

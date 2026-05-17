// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package procnice

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRunLowersNiceness spawns a child that reads field 19 of
// /proc/self/stat (the niceness, per proc(5)) and verifies procnice.Run()
// lowered it to 19.
func TestRunLowersNiceness(t *testing.T) {
	cmd := exec.Command("sh", "-c", "awk '{print $19}' /proc/self/stat")
	out, err := Output(cmd)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "19" {
		t.Errorf("child niceness = %q, want %q", got, "19")
	}
}

// TestRunLowersIonice verifies the child's I/O scheduling class is idle (3)
// after procnice.Run(). Uses /usr/bin/ionice if available; skipped otherwise.
func TestRunLowersIonice(t *testing.T) {
	if _, err := exec.LookPath("ionice"); err != nil {
		t.Skip("ionice not available")
	}
	// Use $$ via shell so ionice sees the shell's pid, which inherits the
	// child's ioprio class set by procnice.Run().
	cmd := exec.Command("sh", "-c", "ionice -p $$")
	out, err := Output(cmd)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if !strings.HasPrefix(got, "idle") {
		t.Errorf("child ionice class = %q, want idle", got)
	}
}

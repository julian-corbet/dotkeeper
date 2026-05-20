// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package procnice

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
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

// TestLowerSelfNicesAllThreads calls LowerSelf() and then checks each
// /proc/self/task/<tid>/stat field 19 (the niceness) to confirm every
// thread in the test binary landed at nice=19. Side-effect: the rest
// of this test binary runs at nice=19 after this test completes —
// acceptable because the test suite makes no scheduling assumptions
// and the daemon's actual workload is what we're modelling.
//
// Forces extra OS threads to exist before the call so the per-thread
// iteration path is exercised (and not just the calling thread).
func TestLowerSelfNicesAllThreads(t *testing.T) {
	// Force a handful of extra OS threads. LockOSThread inside a
	// goroutine pins it to a fresh M, guaranteeing distinct TIDs.
	const extras = 4
	stop := make(chan struct{})
	ready := make(chan struct{}, extras)
	defer close(stop)
	for i := 0; i < extras; i++ {
		go func() {
			runtime.LockOSThread()
			ready <- struct{}{}
			<-stop
		}()
	}
	for i := 0; i < extras; i++ {
		<-ready
	}

	LowerSelf()

	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatalf("readdir /proc/self/task: %v", err)
	}
	if len(entries) < extras+1 {
		t.Fatalf("expected at least %d threads, found %d", extras+1, len(entries))
	}
	var missed []string
	for _, e := range entries {
		data, err := os.ReadFile("/proc/self/task/" + e.Name() + "/stat")
		if err != nil {
			continue // thread exited between readdir and readfile
		}
		// /proc/[pid]/stat format: pid (comm) state ppid pgrp session
		// tty_nr tpgid flags minflt cminflt majflt cmajflt utime stime
		// cutime cstime priority nice ...
		// comm can contain spaces and parens; the closing ')' delimits.
		s := string(data)
		rparen := strings.LastIndexByte(s, ')')
		if rparen < 0 || rparen+1 >= len(s) {
			continue
		}
		fields := strings.Fields(s[rparen+1:])
		// After comm, field indexes shift: field 19 in the full record
		// is index 16 of the post-comm split (0=state, 1=ppid, ...).
		if len(fields) < 17 {
			continue
		}
		nice, err := strconv.Atoi(fields[16])
		if err != nil {
			continue
		}
		if nice != 19 {
			missed = append(missed, e.Name()+"="+fields[16])
		}
	}
	if len(missed) > 0 {
		t.Errorf("LowerSelf missed %d thread(s): %v", len(missed), missed)
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

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWriteFileAtomic_AllOrNothing proves that a concurrent reader either
// observes the previous file contents or the new ones — never a half-written
// intermediate. Runs the writer and reader as tight goroutines; the reader
// reads as fast as it can while the writer overwrites with two distinct
// values, and any observed value that isn't one of those two is a failure.
func TestWriteFileAtomic_AllOrNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	const old = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
	const new = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stop := make(chan struct{})
	bad := make(chan string, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				bad <- fmt.Sprintf("read: %v", err)
				return
			}
			s := string(data)
			if s != old && s != new {
				bad <- fmt.Sprintf("torn read: %q", s)
				return
			}
		}
	}()

	// Hammer the writer between old and new for a beat.
	for i := 0; i < 200; i++ {
		v := []byte(old)
		if i%2 == 0 {
			v = []byte(new)
		}
		if err := WriteFileAtomic(path, v, 0o600); err != nil {
			t.Fatalf("WriteFileAtomic: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	close(bad)
	for msg := range bad {
		t.Error(msg)
	}
}

// TestWriteFileAtomic_NoOrphanTempOnError verifies that when the rename leg
// fails, the temp file is cleaned up so the target directory doesn't
// accumulate `.tmp.*` orphans across many failed writes.
func TestWriteFileAtomic_NoOrphanTempOnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Make the directory read-only so the rename(2) inside WriteFileAtomic
	// is allowed to create the temp (read-only just blocks creation in
	// strict POSIX, but here we use a non-existent subdir which fails the
	// rename target check while permitting temp creation in `dir`).
	// Simulate the failure by passing a path inside a missing subdir.
	missing := filepath.Join(dir, "no-such-subdir", "out.txt")
	err := WriteFileAtomic(missing, []byte("x"), 0o600)
	if err == nil {
		t.Fatal("expected error writing into missing parent directory")
	}
	// Walk the temp dir to ensure no .tmp.* file slipped through.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("orphan temp file: %s", e.Name())
		}
	}
}

// TestWriteFileAtomic_OverwriteReadOnlyTarget proves that the file mode of
// the *target* doesn't block the write — rename(2) only needs write
// permission on the containing directory. This is the behavior assumed by
// the updated TestRealApplierUntrackRepoWriteError after the state.toml
// fix landed.
func TestWriteFileAtomic_OverwriteReadOnlyTarget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "ro.txt")
	if err := os.WriteFile(path, []byte("old"), 0o400); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic into read-only target should succeed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after rename: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("content = %q, want \"new\"", string(data))
	}
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigCache_ReturnsSamePointerOnUnchangedFile verifies that the cache
// returns the same (pointer-equal) parsed value on a second call when the
// underlying file is unchanged — confirming no reparse happened.
func TestConfigCache_ReturnsSamePointerOnUnchangedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "machine.toml")
	if err := os.WriteFile(path, []byte(`schema_version = 2
name = "test"
slot = 0
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := &configCache{}
	first, err := c.loadMachine(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := c.loadMachine(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first != second {
		t.Errorf("expected cached pointer reuse; got distinct values %p vs %p", first, second)
	}
}

// TestConfigCache_RefetchesOnMtimeChange verifies that touching the file
// (changing mtime) triggers a reparse on the next call.
func TestConfigCache_RefetchesOnMtimeChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "machine.toml")
	if err := os.WriteFile(path, []byte(`schema_version = 2
name = "first"
slot = 0
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := &configCache{}
	first, err := c.loadMachine(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Rewrite the file with different content. Bump mtime explicitly because
	// some filesystems coalesce within a second.
	if err := os.WriteFile(path, []byte(`schema_version = 2
name = "second"
slot = 0
`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	second, err := c.loadMachine(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first == second {
		t.Errorf("expected reparse on mtime change; got cached pointer reuse")
	}
	if second.Name != "second" {
		t.Errorf("Name = %q, want %q", second.Name, "second")
	}
}

// TestConfigCache_LoadStateAbsentFileNoRestat checks that a missing state
// file is cached as "not found" and the second call does not error.
func TestConfigCache_LoadStateAbsentFileNoRestat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml") // never created

	c := &configCache{}
	for i := 0; i < 3; i++ {
		v, tracked, err := c.loadState(path)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if v != nil || tracked != nil {
			t.Errorf("call %d: expected (nil, nil) for absent state, got (%v, %v)", i, v, tracked)
		}
	}
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// makeConflict stitches a Conflict struct around a concrete path.
// Tests use this so they can place the "local" and "conflict" files
// in the same directory without inventing a fake DeviceIDShort each time.
func makeConflict(t *testing.T, dir, original string) Conflict {
	t.Helper()
	return Conflict{
		Path:          filepath.Join(dir, original+".sync-conflict-20260419-120000-AAAAAAA"+filepath.Ext(original)),
		OriginalName:  original,
		DeviceIDShort: "AAAAAAA",
	}
}

// TestResolveIdenticalDeletesMatchingFile — hash-identical files are the
// common harmless case. Verify the conflict file is removed and the
// local file is untouched.
func TestResolveIdenticalDeletesMatchingFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "config.toml")
	content := []byte("shared = true\n")
	if err := os.WriteFile(local, content, 0o644); err != nil {
		t.Fatal(err)
	}

	c := makeConflict(t, dir, "config.toml")
	if err := os.WriteFile(c.Path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveIdentical(c)
	if err != nil {
		t.Fatalf("ResolveIdentical: %v", err)
	}
	if got != ActionDeduped {
		t.Fatalf("Action = %q, want %q", got, ActionDeduped)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Errorf("conflict file still present: %v", err)
	}
	if b, err := os.ReadFile(local); err != nil || !bytes.Equal(b, content) {
		t.Errorf("local file altered or missing; err=%v content=%q", err, b)
	}
}

// TestResolveIdenticalKeepsDifferingFiles — when the hashes differ, both
// files stay on disk and the caller gets ActionKeep with no error.
func TestResolveIdenticalKeepsDifferingFiles(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(local, []byte("a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := makeConflict(t, dir, "config.toml")
	if err := os.WriteFile(c.Path, []byte("a = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveIdentical(c)
	if err != nil {
		t.Fatalf("ResolveIdentical: %v", err)
	}
	if got != ActionKeep {
		t.Errorf("Action = %q, want %q", got, ActionKeep)
	}
	if _, err := os.Stat(c.Path); err != nil {
		t.Errorf("conflict file should still exist: %v", err)
	}
}

// TestResolveIdenticalMissingFilesError — neither missing file should
// cause a silent success. Both branches (local, conflict) need coverage
// because they're separate stat calls.
func TestResolveIdenticalMissingFilesError(t *testing.T) {
	dir := t.TempDir()

	// Missing local.
	c := makeConflict(t, dir, "config.toml")
	if err := os.WriteFile(c.Path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveIdentical(c); err == nil {
		t.Error("expected error when local is missing")
	}

	// Missing conflict.
	local := filepath.Join(dir, "other.toml")
	if err := os.WriteFile(local, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := makeConflict(t, dir, "other.toml")
	// Note: we did NOT create c2.Path.
	if _, err := ResolveIdentical(c2); err == nil {
		t.Error("expected error when conflict is missing")
	}
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile is a small helper for setting up scanner fixtures.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanFindsConflicts lays out a tree with a mix of conflict files,
// regular files, and skip-list directories, and verifies the scanner
// returns exactly the conflict files with resolved absolute paths.
func TestScanFindsConflicts(t *testing.T) {
	root := t.TempDir()

	// Conflicts we expect to find.
	want := map[string]bool{
		filepath.Join(root, "config.sync-conflict-20260419-143015-UUS6FSQ.toml"):          false,
		filepath.Join(root, "sub", "notes.sync-conflict-20260419-143015-WB25TET.md"):      false,
		filepath.Join(root, "sub", "deep", "bin.sync-conflict-20260418-185451-WB25TET"):   false,
		filepath.Join(root, ".sync-conflict-20260419-143015-UUS6FSQ.bashrc"):              false,
	}
	for p := range want {
		writeFile(t, p)
	}

	// Noise: regular files.
	writeFile(t, filepath.Join(root, "config.toml"))
	writeFile(t, filepath.Join(root, "sub", "README.md"))

	// Noise: skip-list dirs. Conflicts inside must NOT be returned.
	writeFile(t, filepath.Join(root, ".git", "ignored.sync-conflict-20260419-143015-UUS6FSQ.toml"))
	writeFile(t, filepath.Join(root, ".stfolder", "ignored.sync-conflict-20260419-143015-UUS6FSQ.toml"))
	writeFile(t, filepath.Join(root, ".dkfolder", "ignored.sync-conflict-20260419-143015-UUS6FSQ.toml"))
	// Also: nested skip dir inside a subfolder.
	writeFile(t, filepath.Join(root, "sub", ".git", "ignored.sync-conflict-20260419-143015-UUS6FSQ.toml"))

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(got) != len(want) {
		var paths []string
		for _, c := range got {
			paths = append(paths, c.Path)
		}
		sort.Strings(paths)
		t.Fatalf("got %d conflicts, want %d: %v", len(got), len(want), paths)
	}

	for _, c := range got {
		if _, ok := want[c.Path]; !ok {
			t.Errorf("unexpected conflict returned: %s", c.Path)
			continue
		}
		want[c.Path] = true
		if c.DeviceIDShort == "" {
			t.Errorf("%s: empty DeviceIDShort", c.Path)
		}
		if c.OriginalName == "" {
			t.Errorf("%s: empty OriginalName", c.Path)
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("missing conflict: %s", p)
		}
	}
}

// TestScanEmptyRoot covers a tree with no conflicts at all.
func TestScanEmptyRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.toml"))
	writeFile(t, filepath.Join(root, "sub", "b.md"))

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d conflicts, want 0: %+v", len(got), got)
	}
}

// TestScanMissingRoot confirms Scan surfaces an error when the root
// itself doesn't exist — callers need to distinguish "no conflicts"
// from "nothing to scan".
func TestScanMissingRoot(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

// TestScanReturnsAbsolutePaths verifies that even with a relative input,
// the returned Path values are absolute — downstream code (logging,
// CLI table) relies on that.
func TestScanReturnsAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.sync-conflict-20260419-143015-UUS6FSQ.toml"))

	// Move CWD to root so we can pass "." as a relative path.
	t.Chdir(root)

	got, err := Scan(".")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(got))
	}
	if !filepath.IsAbs(got[0].Path) {
		t.Errorf("Path = %q, want absolute", got[0].Path)
	}
}

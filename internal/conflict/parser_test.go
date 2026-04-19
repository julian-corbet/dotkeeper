// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"errors"
	"testing"
	"time"
)

// TestParseHappyPath covers the canonical conflict shape: name, extension,
// timestamp, and 7-char device ID all present.
func TestParseHappyPath(t *testing.T) {
	got, err := Parse("config.sync-conflict-20260419-143015-UUS6FSQ.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OriginalName != "config.toml" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, "config.toml")
	}
	if got.Extension != ".toml" {
		t.Errorf("Extension = %q, want %q", got.Extension, ".toml")
	}
	if got.DeviceIDShort != "UUS6FSQ" {
		t.Errorf("DeviceIDShort = %q, want %q", got.DeviceIDShort, "UUS6FSQ")
	}
	want := time.Date(2026, 4, 19, 14, 30, 15, 0, time.Local)
	if !got.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, want)
	}
}

// TestParseNoExtension covers extensionless files (e.g. binaries): the
// conflict name has nothing after the 7-char device ID.
func TestParseNoExtension(t *testing.T) {
	got, err := Parse("dotkeeper.sync-conflict-20260418-185451-WB25TET")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OriginalName != "dotkeeper" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, "dotkeeper")
	}
	if got.Extension != "" {
		t.Errorf("Extension = %q, want \"\"", got.Extension)
	}
	if got.DeviceIDShort != "WB25TET" {
		t.Errorf("DeviceIDShort = %q, want %q", got.DeviceIDShort, "WB25TET")
	}
}

// TestParseDotfile covers names like ".bashrc" — Syncthing uses
// filepath.Ext, which treats the whole ".bashrc" as the extension, so
// the conflict filename starts with ".sync-conflict-".
func TestParseDotfile(t *testing.T) {
	got, err := Parse(".sync-conflict-20260419-143015-UUS6FSQ.bashrc")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OriginalName != ".bashrc" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, ".bashrc")
	}
	if got.Extension != ".bashrc" {
		t.Errorf("Extension = %q, want %q", got.Extension, ".bashrc")
	}
	if got.DeviceIDShort != "UUS6FSQ" {
		t.Errorf("DeviceIDShort = %q, want %q", got.DeviceIDShort, "UUS6FSQ")
	}
}

// TestParseNestedPath verifies Parse only looks at the base filename, so
// callers can pass absolute paths directly.
func TestParseNestedPath(t *testing.T) {
	got, err := Parse("/home/user/.agent/notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OriginalName != "notes.md" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, "notes.md")
	}
}

// TestParseDoubleExtension keeps the trailing extension only — matching
// Syncthing's own filepath.Ext behaviour. "file.tar.gz" becomes
// "file.tar" + ".gz" in the conflict shape.
func TestParseDoubleExtension(t *testing.T) {
	got, err := Parse("file.tar.sync-conflict-20260419-143015-UUS6FSQ.gz")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OriginalName != "file.tar.gz" {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, "file.tar.gz")
	}
	if got.Extension != ".gz" {
		t.Errorf("Extension = %q, want %q", got.Extension, ".gz")
	}
}

// TestParseRejectsNonConflict covers plain filenames — must return
// ErrNotConflict so callers can skip them.
func TestParseRejectsNonConflict(t *testing.T) {
	for _, name := range []string{
		"config.toml",
		"README.md",
		".bashrc",
		"dotkeeper",
		"some-file.sync-conflict.toml", // missing timestamp
		"",
	} {
		if _, err := Parse(name); !errors.Is(err, ErrNotConflict) {
			t.Errorf("Parse(%q) err = %v, want ErrNotConflict", name, err)
		}
	}
}

// TestParseRejectsMalformed covers near-miss shapes that must NOT match:
// wrong digit counts, lowercase device IDs, short/long IDs, etc.
func TestParseRejectsMalformed(t *testing.T) {
	cases := []string{
		"config.sync-conflict-2026041-143015-UUS6FSQ.toml",   // short date
		"config.sync-conflict-20260419-14301-UUS6FSQ.toml",   // short time
		"config.sync-conflict-20260419-143015-UUS6FS.toml",   // 6-char ID
		"config.sync-conflict-20260419-143015-UUS6FSQA.toml", // 8-char ID (runs past the 7-char boundary)
		"config.sync-conflict-20260419-143015-uus6fsq.toml",  // lowercase not allowed
		"config.sync-conflict-20260419-143015-UUS6FS1.toml",  // '1' isn't in base32 alphabet
	}
	for _, name := range cases {
		if _, err := Parse(name); !errors.Is(err, ErrNotConflict) {
			t.Errorf("Parse(%q) err = %v, want ErrNotConflict", name, err)
		}
	}
}

// TestParseInvalidDate covers a syntactically correct regex match but an
// impossible calendar date — should error, but not as ErrNotConflict.
func TestParseInvalidDate(t *testing.T) {
	_, err := Parse("config.sync-conflict-20261319-143015-UUS6FSQ.toml") // month 13
	if err == nil {
		t.Fatal("expected error for month 13")
	}
	if errors.Is(err, ErrNotConflict) {
		t.Errorf("got ErrNotConflict, want timestamp parse error: %v", err)
	}
}

// TestIsConflictName is a quick spot check of the helper.
func TestIsConflictName(t *testing.T) {
	cases := map[string]bool{
		"config.sync-conflict-20260419-143015-UUS6FSQ.toml": true,
		"dotkeeper.sync-conflict-20260418-185451-WB25TET":   true,
		"config.toml":    false,
		"random-file":    false,
		".sync-conflict": false,
	}
	for name, want := range cases {
		if got := IsConflictName(name); got != want {
			t.Errorf("IsConflictName(%q) = %v, want %v", name, got, want)
		}
	}
}

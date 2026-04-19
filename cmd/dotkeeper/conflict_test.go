// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// TestDeviceShortToHostname covers the mapping from the 7-char short
// form (as embedded in Syncthing conflict filenames) to the friendly
// hostname registered in the shared config.
func TestDeviceShortToHostname(t *testing.T) {
	cfg := &config.SharedConfig{
		Machines: map[string]config.MachineEntry{
			"desktop": {
				Hostname:    "CACHYOS-Desktop",
				Slot:        0,
				SyncthingID: "UUS6FSQ-ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ23-4567ABC-DEFGHIJ-KLMNOPQ",
			},
			"laptop": {
				Hostname:    "CORBET-ELITEBOOK",
				Slot:        1,
				SyncthingID: "WB25TET-ZYXWVUT-SRQPONM-LKJIHGF-EDCBA76-5432ZYX-WVUTSRQ-PONMLKJ",
			},
			// No SyncthingID yet (freshly-joined, pre-cert-propagation).
			"unknown": {
				Hostname:    "brand-new",
				Slot:        2,
				SyncthingID: "",
			},
		},
	}

	got := deviceShortToHostname(cfg)

	if got["UUS6FSQ"] != "CACHYOS-Desktop" {
		t.Errorf("UUS6FSQ = %q, want %q", got["UUS6FSQ"], "CACHYOS-Desktop")
	}
	if got["WB25TET"] != "CORBET-ELITEBOOK" {
		t.Errorf("WB25TET = %q, want %q", got["WB25TET"], "CORBET-ELITEBOOK")
	}
	// Entries without an ID must not pollute the map (empty key would
	// be returned for every unknown lookup).
	if _, ok := got[""]; ok {
		t.Error("entry with empty SyncthingID should be skipped, not mapped under empty key")
	}
	// An unknown short ID returns the zero value so callers can fall
	// back to the raw short form.
	if host, ok := got["ZZZZZZZ"]; ok {
		t.Errorf("unknown short ID should not be in map, got %q", host)
	}
}

// TestDeviceShortToHostnameEmptyConfig covers the degenerate case where
// the config has no machines registered yet (e.g. very first boot).
func TestDeviceShortToHostnameEmptyConfig(t *testing.T) {
	cfg := &config.SharedConfig{Machines: map[string]config.MachineEntry{}}
	got := deviceShortToHostname(cfg)
	if len(got) != 0 {
		t.Errorf("empty config should yield empty map, got %d entries", len(got))
	}
}

// TestResolveTargetCanonicalPath verifies that passing the canonical
// path finds its single variant.
func TestResolveTargetCanonicalPath(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "notes.md")
	variant := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err := os.WriteFile(canonical, []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(variant, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	variants, gotCanonical, err := resolveTarget(canonical)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if gotCanonical != canonical {
		t.Errorf("canonical = %q, want %q", gotCanonical, canonical)
	}
	if len(variants) != 1 || variants[0].Path != variant {
		t.Errorf("variants = %+v, want [%s]", variants, variant)
	}
}

// TestResolveTargetVariantPath verifies that passing the variant's path
// directly works — the CLI derives the canonical via Parse.
func TestResolveTargetVariantPath(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "notes.md")
	variant := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err := os.WriteFile(canonical, []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(variant, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	variants, gotCanonical, err := resolveTarget(variant)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if gotCanonical != canonical {
		t.Errorf("canonical = %q, want %q", gotCanonical, canonical)
	}
	if len(variants) != 1 || variants[0].Path != variant {
		t.Errorf("variants = %+v, want [%s]", variants, variant)
	}
}

// TestResolveTargetMultipleVariants verifies the three-peer-diverged
// case: passing the canonical returns an error listing each variant.
func TestResolveTargetMultipleVariants(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "notes.md")
	v1 := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	v2 := filepath.Join(dir, "notes.sync-conflict-20260419-150000-WB25TET.md")
	for _, p := range []string{canonical, v1, v2} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := resolveTarget(canonical)
	if err == nil {
		t.Fatal("expected error for multi-variant canonical")
	}
	if !strings.Contains(err.Error(), "2 variants") {
		t.Errorf("error = %v, want mention of '2 variants'", err)
	}
	if !strings.Contains(err.Error(), v1) || !strings.Contains(err.Error(), v2) {
		t.Errorf("error should list both variants: %v", err)
	}

	// Disk untouched.
	for _, p := range []string{canonical, v1, v2} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should still exist: %v", p, err)
		}
	}
}

// TestResolveTargetNoConflict covers paths that aren't conflicts: a
// regular file with no variants, and a non-existent path.
func TestResolveTargetNoConflict(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(canonical, []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		canonical,
		filepath.Join(dir, "does-not-exist.md"),
	}
	for _, p := range cases {
		_, _, err := resolveTarget(p)
		if err == nil {
			t.Errorf("%s: expected error", p)
			continue
		}
		if !strings.Contains(err.Error(), "no conflict") {
			t.Errorf("%s: error = %v, want 'no conflict' message", p, err)
		}
	}
}

// TestResolveTargetMissingVariant covers a variant path that doesn't
// exist on disk. The CLI should treat this as "no conflict" rather
// than "file not readable".
func TestResolveTargetMissingVariant(t *testing.T) {
	dir := t.TempDir()
	variant := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	_, _, err := resolveTarget(variant)
	if err == nil {
		t.Fatal("expected error for missing variant")
	}
	if !strings.Contains(err.Error(), "no conflict") {
		t.Errorf("error = %v, want 'no conflict' message", err)
	}
}

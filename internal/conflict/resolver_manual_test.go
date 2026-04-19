// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// gitHEADSubject returns the subject line of HEAD. Used by Accept tests
// to verify the commit message shape.
func gitHEADSubject(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%s") // #nosec G204 -- fixed test fixture
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitHEADFiles returns the list of file paths changed by HEAD. Used to
// verify that Accept's commit is scoped to a single file.
func gitHEADFiles(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "show", "--name-only", "--format=", "HEAD") // #nosec G204 -- fixed test fixture
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, out)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files
}

// TestKeepRemovesVariant covers the happy path for Keep: delete the
// conflict file, leave the canonical alone.
func TestKeepRemovesVariant(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "notes.md")
	variant := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")

	if err := os.WriteFile(canonical, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(variant, []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Conflict{
		Path:          variant,
		OriginalName:  "notes.md",
		Timestamp:     time.Now(),
		DeviceIDShort: "UUS6FSQ",
		Extension:     ".md",
	}
	if err := Keep(c); err != nil {
		t.Fatalf("Keep: %v", err)
	}

	if _, err := os.Stat(variant); !os.IsNotExist(err) {
		t.Errorf("variant should be gone, err = %v", err)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(data) != "original\n" {
		t.Errorf("canonical was modified: %q", data)
	}
}

// TestKeepIdempotent verifies that calling Keep on an already-removed
// variant succeeds silently — the command can be safely re-run.
func TestKeepIdempotent(t *testing.T) {
	dir := t.TempDir()
	variant := filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")

	c := Conflict{
		Path:          variant,
		OriginalName:  "notes.md",
		DeviceIDShort: "UUS6FSQ",
	}
	// Variant was never written — Keep should still succeed.
	if err := Keep(c); err != nil {
		t.Errorf("Keep on missing variant = %v, want nil", err)
	}
}

// TestAcceptOverwritesAndCommits covers the happy path: variant content
// lands on the canonical, variant is gone, single scoped commit exists.
func TestAcceptOverwritesAndCommits(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "notes.md", "local\n", "initial")

	// Add a second unrelated file (uncommitted) to verify the accept
	// commit doesn't sweep it up.
	other := filepath.Join(repo, "other.md")
	if err := os.WriteFile(other, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Drop the variant next to the canonical.
	variant := filepath.Join(repo, "notes.sync-conflict-20260419-143015-UUS6FSQ.md")
	if err := os.WriteFile(variant, []byte("from-peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Conflict{
		Path:          variant,
		OriginalName:  "notes.md",
		Timestamp:     time.Now(),
		DeviceIDShort: "UUS6FSQ",
		Extension:     ".md",
	}
	if err := Accept(testCtx(t), c, repo); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// Canonical has the variant's bytes.
	data, err := os.ReadFile(filepath.Join(repo, "notes.md"))
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(data) != "from-peer\n" {
		t.Errorf("canonical = %q, want %q", data, "from-peer\n")
	}
	// Variant is gone.
	if _, err := os.Stat(variant); !os.IsNotExist(err) {
		t.Errorf("variant should be gone, err = %v", err)
	}
	// HEAD subject matches the required shape.
	subj := gitHEADSubject(t, repo)
	wantSubj := "auto: accept sync conflict for notes.md (from UUS6FSQ)"
	if subj != wantSubj {
		t.Errorf("HEAD subject = %q, want %q", subj, wantSubj)
	}
	// HEAD touched only the canonical — the other dirty file stayed
	// out of the commit.
	files := gitHEADFiles(t, repo)
	if len(files) != 1 || files[0] != "notes.md" {
		t.Errorf("HEAD files = %v, want [notes.md]", files)
	}
	// And other.md is still on disk, still uncommitted.
	if _, err := os.Stat(other); err != nil {
		t.Errorf("other.md disappeared: %v", err)
	}
}

// TestAcceptIdempotent covers a second invocation after a successful
// accept — variant is gone, Accept should short-circuit with nil and
// produce no new commit.
func TestAcceptIdempotent(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	gitCommit(t, repo, "notes.md", "from-peer\n", "initial")

	headBefore := gitHEADSubject(t, repo)

	// Variant was never written — this mimics "I already ran accept".
	c := Conflict{
		Path:          filepath.Join(repo, "notes.sync-conflict-20260419-143015-UUS6FSQ.md"),
		OriginalName:  "notes.md",
		DeviceIDShort: "UUS6FSQ",
	}
	if err := Accept(testCtx(t), c, repo); err != nil {
		t.Fatalf("Accept (idempotent): %v", err)
	}

	headAfter := gitHEADSubject(t, repo)
	if headAfter != headBefore {
		t.Errorf("HEAD changed on idempotent Accept: before=%q after=%q", headBefore, headAfter)
	}
}

// TestAcceptPreservesMode verifies that Accept keeps the executable bit
// (and other mode bits) on the canonical file. Losing +x on a merged
// script is exactly the kind of subtle regression we want to guard
// against.
func TestAcceptPreservesMode(t *testing.T) {
	repo := gitInit(t, t.TempDir())
	canonical := filepath.Join(repo, "run.sh")
	if err := os.WriteFile(canonical, []byte("#!/bin/sh\necho local\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "run.sh", "#!/bin/sh\necho local\n", "initial")
	if err := os.Chmod(canonical, 0o755); err != nil {
		t.Fatal(err)
	}

	variant := filepath.Join(repo, "run.sync-conflict-20260419-143015-UUS6FSQ.sh")
	if err := os.WriteFile(variant, []byte("#!/bin/sh\necho peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Conflict{
		Path:          variant,
		OriginalName:  "run.sh",
		DeviceIDShort: "UUS6FSQ",
		Extension:     ".sh",
	}
	if err := Accept(testCtx(t), c, repo); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	info, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("+x bit lost on canonical: mode = %v", info.Mode().Perm())
	}
}

// TestFindVariantsSingle covers the common case: one conflict for one
// canonical file, picked out of a directory with unrelated siblings.
func TestFindVariantsSingle(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notes.md"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md"), []byte{}, 0o644)
	// Unrelated noise — must not be returned.
	_ = os.WriteFile(filepath.Join(dir, "other.md"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "other.sync-conflict-20260419-143015-WB25TET.md"), []byte{}, 0o644)

	got, err := FindVariants(filepath.Join(dir, "notes.md"))
	if err != nil {
		t.Fatalf("FindVariants: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d variants, want 1: %+v", len(got), got)
	}
	if got[0].OriginalName != "notes.md" {
		t.Errorf("OriginalName = %q, want notes.md", got[0].OriginalName)
	}
}

// TestFindVariantsMultiple covers the rare three-peer-diverged case:
// two conflict files fork off the same canonical. FindVariants returns
// both so the CLI can tell the user "pass one explicitly".
func TestFindVariantsMultiple(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notes.md"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.sync-conflict-20260419-143015-UUS6FSQ.md"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.sync-conflict-20260419-150000-WB25TET.md"), []byte{}, 0o644)

	got, err := FindVariants(filepath.Join(dir, "notes.md"))
	if err != nil {
		t.Fatalf("FindVariants: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d variants, want 2", len(got))
	}
}

// TestFindVariantsNone returns an empty slice (not an error) when the
// canonical has no conflicts. Distinguishes cleanly from I/O errors.
func TestFindVariantsNone(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notes.md"), []byte{}, 0o644)

	got, err := FindVariants(filepath.Join(dir, "notes.md"))
	if err != nil {
		t.Fatalf("FindVariants: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d variants, want 0", len(got))
	}
}

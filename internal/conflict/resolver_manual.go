// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Keep removes the sync-conflict variant and leaves the canonical file
// untouched. No git activity — keeping "what's already there" doesn't
// change anything tracked.
//
// Idempotent: a missing variant is treated as success (nil error) so
// re-running the command after a previous resolve is safe.
func Keep(c Conflict) error {
	if err := os.Remove(c.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove conflict %s: %w", c.Path, err)
	}
	return nil
}

// Accept replaces the canonical file's contents with the variant's
// contents, removes the variant, and produces a scoped git commit with
// the message
//
//	auto: accept sync conflict for <relpath> (from <deviceShort>)
//
// The canonical file's mode is preserved. The commit touches only the
// canonical path (git commit --only), matching the behaviour of the
// auto-resolver's clean-merge commits.
//
// repoRoot is the absolute path of the git repo containing the
// canonical file — typically the managed-folder root. If the path is
// outside repoRoot, Accept returns an error without touching disk.
//
// Accept is idempotent when the canonical already matches the variant
// and the variant is gone (previous successful accept): it detects the
// missing variant up-front and returns nil, leaving the tree untouched.
func Accept(ctx context.Context, c Conflict, repoRoot string) error {
	local := localFilePath(c)

	// Idempotency: variant already cleaned up means a previous run
	// landed the accept. Nothing to do.
	if _, err := os.Stat(c.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat conflict %s: %w", c.Path, err)
	}

	// Path-safety check: canonical must live inside repoRoot.
	relPath, err := filepath.Rel(repoRoot, local)
	if err != nil {
		return fmt.Errorf("rel path for %s under %s: %w", local, repoRoot, err)
	}
	if relPath == ".." || (len(relPath) >= 3 && relPath[:3] == ".."+string(filepath.Separator)) {
		return fmt.Errorf("canonical path %s is outside repoRoot %s", local, repoRoot)
	}
	relPathGit := filepath.ToSlash(relPath)

	variantData, err := os.ReadFile(c.Path) // #nosec G304 -- absolute scanner path
	if err != nil {
		return fmt.Errorf("read conflict %s: %w", c.Path, err)
	}

	// Preserve the canonical file's permissions. If the canonical is
	// missing entirely — rare but possible when the user has deleted
	// it and only the variant remains — fall back to a sensible 0o644.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(local); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat local %s: %w", local, err)
	}

	if err := writeAtomic(local, variantData, mode); err != nil {
		return fmt.Errorf("write accepted %s: %w", local, err)
	}
	if err := os.Remove(c.Path); err != nil {
		return fmt.Errorf("remove conflict %s: %w", c.Path, err)
	}

	// Stage and commit only the accepted file. Mirror the arg order
	// used by applyCleanMerge so git's pathspec parsing stays happy.
	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", "--", relPathGit)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %w: %s", relPathGit, err, bytes.TrimSpace(out))
	}
	msg := fmt.Sprintf("auto: accept sync conflict for %s (from %s)", relPathGit, c.DeviceIDShort)
	commitCmd := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"commit", "--only", "-m", msg, "--", relPathGit)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit %s: %w: %s", relPathGit, err, bytes.TrimSpace(out))
	}
	return nil
}

// FindVariants returns every sync-conflict variant currently sharing a
// canonical file. The canonical path must be absolute. Only the parent
// directory is scanned, so this is cheap even under large trees.
//
// The ordering matches os.ReadDir (filesystem-dependent but stable
// per call) — callers that need a deterministic order should sort.
func FindVariants(canonical string) ([]Conflict, error) {
	dir := filepath.Dir(canonical)
	canonicalBase := filepath.Base(canonical)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []Conflict
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		c, err := Parse(e.Name())
		if err != nil {
			continue
		}
		if c.OriginalName != canonicalBase {
			continue
		}
		c.Path = filepath.Join(dir, e.Name())
		out = append(out, *c)
	}
	return out, nil
}

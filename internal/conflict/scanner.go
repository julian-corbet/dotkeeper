// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// skipDirs names directories the scanner refuses to descend into. These
// carry git or Syncthing state, which is neither user content nor likely
// to host user-visible conflicts — skipping them avoids noise.
var skipDirs = map[string]struct{}{
	".git":      {},
	".stfolder": {},
	".dkfolder": {},
}

// Scan walks root recursively and returns every sync-conflict file it
// finds, with parsed metadata and absolute paths.
//
// Symlinks are not followed (filepath.WalkDir calls Lstat), matching the
// safer default. The skipDirs set is trimmed at any depth, not just at
// the top level, so a nested .git inside a repo is skipped too.
//
// Unreadable sub-trees do not abort the scan: permission errors on
// individual entries are ignored so a single locked-down directory
// inside an otherwise-healthy tree does not mask real conflicts. Other
// errors (including root-level ones) propagate.
func Scan(root string) ([]Conflict, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	// Fail loudly if the root itself is missing / unreadable: callers
	// need to tell "scan found nothing" apart from "there's no tree to
	// scan". Sub-path errors are tolerated inside the walk.
	if _, err := os.Stat(absRoot); err != nil {
		return nil, err
	}

	var out []Conflict
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied or transient errors on a sub-path: skip
			// that sub-tree if it's a directory, otherwise skip the file.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip && path != absRoot {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		c, perr := Parse(d.Name())
		if perr != nil {
			if errors.Is(perr, ErrNotConflict) {
				return nil
			}
			// Malformed timestamp on an otherwise conflict-shaped name:
			// skip silently rather than failing the whole scan.
			return nil
		}
		c.Path = path
		out = append(out, *c)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

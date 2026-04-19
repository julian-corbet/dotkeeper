// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Action is the outcome of an auto-resolution attempt. Callers use this
// to decide whether to try the next resolver in the chain and to produce
// a single log line per event.
type Action string

const (
	// ActionDeduped means the conflict file was a byte-for-byte duplicate
	// of the local file and has been removed. No data lost.
	ActionDeduped Action = "deduped"

	// ActionMerged means the conflict was resolved by a 3-way merge, the
	// local file was overwritten with the merged content, the conflict
	// file deleted, and the result committed to git.
	ActionMerged Action = "merged"

	// ActionKeep means no automatic resolution was attempted or possible;
	// both files remain on disk for the user to resolve manually. The
	// caller should log a reason to aid debugging.
	ActionKeep Action = "kept"
)

// localFilePath returns the absolute path to the file a conflict was
// forked from. Syncthing places the conflict file in the same directory
// as the original, so we just swap in OriginalName.
func localFilePath(c Conflict) string {
	return filepath.Join(filepath.Dir(c.Path), c.OriginalName)
}

// hashFile returns the SHA-256 of a file's contents. Streams the read so
// huge files don't balloon memory.
func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- path is a scanner-produced absolute path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// ResolveIdentical removes the conflict file if it is byte-identical to
// the local (original) file. This is by far the commonest harmless case:
// two machines made the same save (e.g. editor-on-save reformat) and
// Syncthing couldn't tell they were equivalent.
//
// Returns:
//   - ActionDeduped + nil: conflict file was deleted.
//   - ActionKeep + nil: files differ; caller should try the next resolver.
//   - ActionKeep + error: something went wrong reading either file
//     (missing, permission denied, etc.); caller should log and move on.
func ResolveIdentical(c Conflict) (Action, error) {
	local := localFilePath(c)

	if _, err := os.Stat(local); err != nil {
		return ActionKeep, fmt.Errorf("stat local %s: %w", local, err)
	}
	if _, err := os.Stat(c.Path); err != nil {
		return ActionKeep, fmt.Errorf("stat conflict %s: %w", c.Path, err)
	}

	hLocal, err := hashFile(local)
	if err != nil {
		return ActionKeep, fmt.Errorf("hash local %s: %w", local, err)
	}
	hConflict, err := hashFile(c.Path)
	if err != nil {
		return ActionKeep, fmt.Errorf("hash conflict %s: %w", c.Path, err)
	}

	if !bytes.Equal(hLocal, hConflict) {
		return ActionKeep, nil
	}

	if err := os.Remove(c.Path); err != nil {
		return ActionKeep, fmt.Errorf("remove conflict %s: %w", c.Path, err)
	}
	return ActionDeduped, nil
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// WriteFileAtomic writes data to a sibling temp file in the same directory as
// path and renames it into place. Rename is atomic on Linux/Unix within a
// single filesystem, which is always true here because the temp file is
// created next to the target. This guarantees a concurrent reader either sees
// the previous contents in full or the new contents in full — never a
// half-written intermediate.
//
// On any failure during write/sync/close, the temp file is removed so a
// crash never leaves a `<path>.tmp.*` orphan around.
//
// Race semantics: two concurrent atomic writes still have last-writer-wins
// for the file contents. Callers that need lost-update safety (read, mutate,
// write) must wrap their cycle with an exclusive lock — see MutateStateV2
// for the pattern.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("WriteFileAtomic: rand: %w", err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%s", path, os.Getpid(), hex.EncodeToString(nonce[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("WriteFileAtomic: create temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("WriteFileAtomic: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("WriteFileAtomic: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("WriteFileAtomic: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("WriteFileAtomic: rename: %w", err)
	}
	return nil
}

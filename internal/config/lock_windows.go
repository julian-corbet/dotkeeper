// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes an exclusive byte-range lock on the entire file
// using Windows' LockFileEx. This is mandatory locking on Windows (unlike
// Unix's advisory flock), so other processes attempting to write the file
// directly will fail rather than silently interleave. That matches the
// contract MutateStateV2 needs for state.toml.
//
// The lock is held until the file handle closes (process exit or explicit
// Close), same as flock. We do not specify LOCKFILE_FAIL_IMMEDIATELY so the
// call blocks until the lock is granted, matching the Unix semantics.
func lockFileExclusive(f *os.File) error {
	// Lock the entire file: offset=0, length=MaxInt64 (in two 32-bit halves).
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		^uint32(0), ^uint32(0), // lock all bytes (low + high DWORD = max uint64)
		&ol,
	)
}

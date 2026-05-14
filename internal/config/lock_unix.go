// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows

package config

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive takes an exclusive advisory lock on the given file.
// On Unix this is flock(LOCK_EX), which is process-scoped: closing the file
// (or the process exiting) releases the lock. The lock is automatically
// inherited across fork but NOT across an `execve` of an unrelated binary,
// which is exactly the contract MutateStateV2 needs.
//
// flock blocks until the lock is granted; there is no timeout. Concurrent
// dotkeeper invocations serialize naturally and never deadlock because
// holders never wait on each other while holding the lock.
func lockFileExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

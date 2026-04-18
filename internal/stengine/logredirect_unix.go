// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package stengine

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// stateDir returns ~/.local/state/dotkeeper/ (XDG_STATE_HOME/dotkeeper).
func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dotkeeper")
}

// redirectSyncthingLogs routes Syncthing's stdout-bound log output to
// ~/.local/state/dotkeeper/syncthing.log. Returns an *os.File wrapping the
// original stdout so dotkeeper's own output can still reach the user.
//
// Syncthing's logger captures os.Stdout at package init and offers no
// SetOutput hook. We therefore dup the real fd 1 (to preserve dotkeeper's
// stdout) and dup2 the log file onto fd 1, so anything the captured
// os.Stdout *os.File writes lands in the log file instead.
//
// x/sys/unix.Dup2 is used (not syscall.Dup2) because the stdlib syscall
// package omits Dup2 on Linux arches where only the Dup3 syscall exists
// (arm64, riscv64, 386, etc.). x/sys/unix abstracts this.
func redirectSyncthingLogs() (*os.File, error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	logPath := filepath.Join(dir, "syncthing.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening syncthing.log: %w", err)
	}

	// Save the original fd 1 so dotkeeper's own output still goes to
	// the real stdout (systemd journal, user terminal).
	origFD, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		// Cleanup errors here are swallowed — the outer dup failure is what
		// the caller needs to see.
		_ = logFile.Close()
		return nil, fmt.Errorf("dup stdout: %w", err)
	}

	// Point fd 1 at the log file. Syncthing's captured *os.File for stdout
	// still writes via fd 1, so its output now lands in the log file.
	if err := unix.Dup2(int(logFile.Fd()), int(os.Stdout.Fd())); err != nil {
		_ = logFile.Close()
		_ = unix.Close(origFD)
		return nil, fmt.Errorf("dup2 stdout: %w", err)
	}
	// The log file's fd is no longer needed — fd 1 is the duplicate.
	_ = logFile.Close()

	return os.NewFile(uintptr(origFD), "dotkeeper-stdout"), nil
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package stengine

import (
	"os"
	"path/filepath"
)

// stateDir returns %LOCALAPPDATA%\dotkeeper\state or the home-dir fallback.
func stateDir() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "dotkeeper", "state")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Local", "dotkeeper", "state")
}

// redirectSyncthingLogs is a no-op on Windows. Console fd redirection requires
// SetStdHandle / duplicate-handle gymnastics that are not worth implementing
// until there is a Windows user who cares. Syncthing's log output goes to
// whatever stdout the process inherited.
func redirectSyncthingLogs() (*os.File, error) {
	return os.Stdout, nil
}

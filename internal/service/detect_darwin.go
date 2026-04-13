// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package service

// Detect returns launchd on macOS.
func Detect() (Manager, error) {
	return &Launchd{}, nil
}

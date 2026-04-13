// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package service

// Detect returns Task Scheduler on Windows.
func Detect() (Manager, error) {
	return &WinTaskSched{}, nil
}

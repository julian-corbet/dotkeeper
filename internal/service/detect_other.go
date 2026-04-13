// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin && !windows

package service

// Detect returns cron as the fallback on BSDs and other Unix systems.
func Detect() (Manager, error) {
	return &Cron{}, nil
}

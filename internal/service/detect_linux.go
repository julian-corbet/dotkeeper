// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package service

// Detect returns systemd if available, cron otherwise (Alpine, Void, Gentoo).
func Detect() (Manager, error) {
	if isSystemd() {
		return &Systemd{}, nil
	}
	return &Cron{}, nil
}

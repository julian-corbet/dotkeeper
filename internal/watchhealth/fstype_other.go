// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin && !windows

package watchhealth

// detectFilesystem stubs out on BSDs and exotic platforms. Returns
// Unknown so reconcile falls back to periodic rescans — the safe
// conservative behaviour. kqueue-based platforms (FreeBSD, OpenBSD,
// NetBSD) are functional but not first-class for dotkeeper; if real
// users appear, add a per-platform file mirroring fstype_linux.go.
func detectFilesystem(_ string) (FilesystemKind, string) {
	return FilesystemUnknown, ""
}

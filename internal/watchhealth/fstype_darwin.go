// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package watchhealth

import "golang.org/x/sys/unix"

// darwinReliable lists filesystems for which FSEvents fires
// dependably for files under our control. APFS is the modern Mac
// default; HFS+ remains on older or external volumes.
var darwinReliable = map[string]bool{
	"apfs":  true,
	"hfs":   true,
	"msdos": true, // FAT on a USB stick; FSEvents fires for local writes
	"exfat": true,
}

// darwinUnreliable lists filesystems for which FSEvents is documented
// not to fire (network mounts) or where the implementation is too
// inconsistent across kernel versions to trust.
//
// Apple's FSEvents documentation is explicit that events are not
// generated for mounted network filesystems. This is the single most
// important entry on this table for Mac users.
var darwinUnreliable = map[string]bool{
	"nfs":     true,
	"smbfs":   true,
	"afpfs":   true,
	"webdav":  true,
	"autofs":  true,
	"osxfuse": true,
	"macfuse": true,
}

func detectFilesystem(path string) (FilesystemKind, string) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return FilesystemUnknown, ""
	}
	// On macOS, Fstypename is a fixed-size byte array. Trim trailing
	// zeros to recover the conventional short name ("apfs", "nfs").
	name := cString(st.Fstypename[:])
	if darwinReliable[name] {
		return FilesystemReliable, name
	}
	if darwinUnreliable[name] {
		return FilesystemUnreliable, name
	}
	return FilesystemUnknown, name
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

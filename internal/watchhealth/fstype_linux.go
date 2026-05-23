// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package watchhealth

import "golang.org/x/sys/unix"

// linuxFilesystemKind classifies by the f_type field of statfs(2).
// Magic numbers from <linux/magic.h>. Kept as integer constants
// rather than syscall package re-exports because the syscall package
// covers only a small subset; we need filesystems that didn't make
// the cut (zfs, virtiofs, fuseblk).
//
// When extending: prefer to err on the side of "Unreliable". The
// cost of misclassifying a healthy FS as unreliable is one extra
// scheduled rescan per day. The cost of misclassifying an
// unreliable FS as reliable is silent peer divergence — the
// failure mode that started this whole investigation.
var linuxReliableMagic = map[int64]string{
	0xef53:     "ext4", // also matches ext2, ext3 — same magic
	0x9123683e: "btrfs",
	0x58465342: "xfs",
	0x2fc12fc1: "zfs",
	0x01021994: "tmpfs",
	0xf2f52010: "f2fs",
	0x9fa0:     "proc",      // technically reliable but pointless to sync
	0x73717368: "squashfs",  // read-only; no events to miss
	0x794c7630: "overlayfs", // events on the upperdir layer fire normally
}

var linuxUnreliableMagic = map[int64]string{
	0x6969:     "nfs",
	0xff534d42: "cifs",
	0x517b:     "smbfs",
	0x01021997: "v9fs", // 9p
	0x65735546: "fuse", // generic FUSE; some fuse FS work, some don't, treat as unreliable
	0x65735543: "fuseblk",
	0x73757245: "virtiofs", // host-driven; guest's inotify may or may not see host writes
	0x52654973: "reiserfs", // events have historical issues; safer to rescan
}

func detectFilesystem(path string) (FilesystemKind, string) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		// We can't statfs (path doesn't exist or permission denied).
		// Returning Unknown defers the decision; reconcile will
		// re-check on the next cycle, by which time the path may be
		// resolvable.
		return FilesystemUnknown, ""
	}
	// Statfs_t.Type is int64 on amd64 but uint32 on i386; coerce to
	// the type the maps use.
	magic := int64(st.Type) //nolint:unconvert // platform-specific width
	if name, ok := linuxReliableMagic[magic]; ok {
		return FilesystemReliable, name
	}
	if name, ok := linuxUnreliableMagic[magic]; ok {
		return FilesystemUnreliable, name
	}
	// Unknown magic number: report the hex so log messages let us
	// extend the tables without a debugger.
	return FilesystemUnknown, formatMagic(magic)
}

func formatMagic(magic int64) string {
	const hex = "0123456789abcdef"
	out := []byte("0x00000000")
	for i := 0; i < 8; i++ {
		out[9-i] = hex[(magic>>(i*4))&0xf]
	}
	return string(out)
}

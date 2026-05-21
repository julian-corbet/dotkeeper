// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package watchhealth

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsReliable lists filesystems for which ReadDirectoryChangesW
// fires dependably. NTFS is the default for Windows installs since
// Vista; ReFS is its successor for server/Pro SKUs. FAT and exFAT
// are common on removable media — events fire for local writes,
// though unplugging the media while the handle is open can break
// the watcher (separate concern).
var windowsReliable = map[string]bool{
	"NTFS":  true,
	"ReFS":  true,
	"FAT32": true,
	"exFAT": true,
	"FAT":   true,
}

// windowsUnreliable lists FS-names known to back network shares or
// otherwise sit outside the local kernel's view. Note that SMB
// shares often present as "NTFS" in GetVolumeInformation but with
// the FILE_REMOTE_DEVICE flag set; we don't detect that yet —
// future enhancement: GetDriveType on the volume root and reject
// DRIVE_REMOTE.
var windowsUnreliable = map[string]bool{
	"CIFS":   true,
	"SMB":    true,
	"NFS":    true,
	"WebDAV": true,
}

func detectFilesystem(path string) (FilesystemKind, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return FilesystemUnknown, ""
	}
	// GetVolumeInformation requires the volume root (e.g. "C:\");
	// trim path to the volume mount.
	volumeRoot := filepath.VolumeName(abs)
	if volumeRoot == "" {
		return FilesystemUnknown, ""
	}
	// Volume root must end in a separator for the Win32 API.
	if !strings.HasSuffix(volumeRoot, `\`) {
		volumeRoot += `\`
	}
	pathPtr, err := windows.UTF16PtrFromString(volumeRoot)
	if err != nil {
		return FilesystemUnknown, ""
	}

	var (
		fsNameBuf  [windows.MAX_PATH + 1]uint16
		volNameBuf [windows.MAX_PATH + 1]uint16
		serialNum  uint32
		maxComp    uint32
		flags      uint32
	)
	err = windows.GetVolumeInformation(
		pathPtr,
		&volNameBuf[0], uint32(len(volNameBuf)),
		&serialNum, &maxComp, &flags,
		&fsNameBuf[0], uint32(len(fsNameBuf)),
	)
	if err != nil {
		return FilesystemUnknown, ""
	}
	fsName := windows.UTF16ToString(fsNameBuf[:])

	// Cross-check with GetDriveType: any remote drive is unreliable
	// regardless of what FS name it reports. Many SMB shares
	// advertise as "NTFS" because the server is Windows.
	if driveTypeIsRemote(pathPtr) {
		return FilesystemUnreliable, fsName + " (network)"
	}

	if windowsReliable[fsName] {
		return FilesystemReliable, fsName
	}
	if windowsUnreliable[fsName] {
		return FilesystemUnreliable, fsName
	}
	return FilesystemUnknown, fsName
}

const driveRemote uint32 = 4 // DRIVE_REMOTE from winbase.h

func driveTypeIsRemote(rootPtr *uint16) bool {
	dt := windows.GetDriveType(rootPtr)
	return dt == driveRemote
}

// Keep unsafe import live in case future fields require pointer
// arithmetic for VolumeInfoEx. Removing the line if unused causes
// build noise on platforms where the file is conditionally compiled.
var _ unsafe.Pointer

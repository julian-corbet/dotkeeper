// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package activity

import "os"

// statNoFollow is os.Lstat with a more discoverable name in the
// context of "should we descend into this newly-created entry?" —
// following the symlink would let a malicious repo trick the tracker
// into watching directories outside its scan roots.
func statNoFollow(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import "os"

// statNoFollow returns file info without dereferencing symlinks. The
// watcher uses it when classifying a Create event — a symlinked dir
// would recurse into unbounded territory if we followed it.
func statNoFollow(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

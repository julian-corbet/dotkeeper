// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package conflict parses, scans, and watches for Syncthing sync-conflict
// files in dotkeeper-managed folders.
//
// Syncthing produces conflict files in the shape
//
//	<base>.sync-conflict-YYYYMMDD-HHMMSS-<7charDeviceID><ext>
//
// where <ext> is whatever filepath.Ext returned on the original filename
// (may be empty). This package detects them so dotkeeper can surface the
// conflict to the user instead of letting the losing version sit silently
// in the tree.
package conflict

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// ErrNotConflict is returned by Parse when the filename does not match
// Syncthing's sync-conflict shape. It's a sentinel so callers (e.g. the
// scanner) can skip non-conflict files cleanly.
var ErrNotConflict = errors.New("not a sync-conflict filename")

// Conflict describes a single Syncthing sync-conflict file.
//
// Path is the absolute filesystem path (set by the scanner/watcher) —
// Parse itself leaves it empty since it only sees a filename.
type Conflict struct {
	// Path is the absolute path to the conflict file. Empty when produced
	// by Parse alone; populated by Scan and Watcher.
	Path string

	// OriginalName is the filename the conflict was forked from, e.g.
	// "config.toml" for "config.sync-conflict-*.toml", or ".bashrc" for
	// ".sync-conflict-*.bashrc".
	OriginalName string

	// Timestamp is the conflict time, parsed from the YYYYMMDD-HHMMSS
	// fragment. Syncthing writes this in local time with no timezone
	// information, so we parse it as time.Local.
	Timestamp time.Time

	// DeviceIDShort is the 7-character base32 short form of the device
	// that produced the losing version.
	DeviceIDShort string

	// Extension is the trailing extension (including the leading dot),
	// or "" if the original file had no extension.
	Extension string
}

// conflictRE matches Syncthing's conflict filename shape. Breakdown:
//
//	^(.*?)                        — original base (non-greedy, may be empty for dotfiles)
//	\.sync-conflict-              — literal marker
//	(\d{8})-(\d{6})               — date (YYYYMMDD) and time (HHMMSS)
//	-([A-Z2-7]{7})                — short device ID (base32 upper alphabet)
//	(\.[^/]*)?$                   — optional extension
//
// Anchoring with ^ and $ ensures we match the entire base filename, not a
// substring somewhere in the path.
var conflictRE = regexp.MustCompile(`^(.*?)\.sync-conflict-(\d{8})-(\d{6})-([A-Z2-7]{7})(\.[^/]*)?$`)

// conflictTimeLayout is the Go reference time matching YYYYMMDD-HHMMSS.
const conflictTimeLayout = "20060102-150405"

// Parse extracts conflict metadata from a filename (or path — only the
// base is examined). Returns ErrNotConflict if the name isn't in
// Syncthing's conflict shape.
//
// Parse does not touch the filesystem.
func Parse(filename string) (*Conflict, error) {
	base := filepath.Base(filename)
	m := conflictRE.FindStringSubmatch(base)
	if m == nil {
		return nil, ErrNotConflict
	}

	// m[1] = original stem (may be empty for dotfiles)
	// m[2] = date, m[3] = time
	// m[4] = 7-char short device ID
	// m[5] = extension incl. leading dot, or "" if absent
	stem, date, timeStr, devID, ext := m[1], m[2], m[3], m[4], m[5]

	ts, err := time.ParseInLocation(conflictTimeLayout, date+"-"+timeStr, time.Local)
	if err != nil {
		// Regex already enforced the digit shape, so parse failure means
		// impossible calendar values (e.g. month 13). Report it.
		return nil, fmt.Errorf("parsing timestamp %q: %w", date+"-"+timeStr, err)
	}

	return &Conflict{
		OriginalName:  stem + ext,
		Timestamp:     ts,
		DeviceIDShort: devID,
		Extension:     ext,
	}, nil
}

// IsConflictName reports whether the given filename is a sync-conflict
// filename. Cheap helper when callers don't need the parsed fields.
func IsConflictName(filename string) bool {
	return conflictRE.MatchString(filepath.Base(filename))
}

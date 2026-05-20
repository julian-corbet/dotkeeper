// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package watchhealth answers one question for each managed folder:
// "Is the OS filesystem-event API reliable enough that dotkeeper can
// trust it as the sole source of change notifications, or do we need
// to schedule periodic rescans as a safety net?"
//
// The question is platform-specific because the underlying API is:
//
//   - Linux:   inotify (per-subdirectory watches, kernel queue with
//              overflow signal, per-user watch + instance limits,
//              well-defined behaviour on each filesystem)
//   - macOS:   FSEvents (per-tree subscription, coalesces events with
//              configurable latency, persistent history buffer for
//              missed events, *does not fire on network mounts at all*)
//   - Windows: ReadDirectoryChangesW (per-handle, optional recursion,
//              fixed user-mode buffer that returns
//              ERROR_NOTIFY_ENUM_DIR on overflow, limited support on
//              SMB shares)
//   - BSDs:    kqueue + EVFILT_VNODE (per-fd; hits fd limits faster
//              than Linux hits watch limits)
//
// The package abstracts these into a single Status type and exposes
// detectors that the reconcile diff can consult without knowing
// which OS it's running on.
package watchhealth

import "time"

// FilesystemKind classifies the storage backend a folder lives on
// well enough to decide whether the event API can be trusted.
// Unknown is the safe-default classification: when we can't recognize
// the filesystem (custom mount, exotic kernel, build error in
// platform-specific detection), we treat it as unreliable and rely
// on periodic rescans. False positives (treating a healthy FS as
// unreliable) cost a daily scan; false negatives (trusting a
// network mount that doesn't fire events) cost silent divergence
// between peers.
type FilesystemKind int

const (
	FilesystemUnknown FilesystemKind = iota
	FilesystemReliable
	FilesystemUnreliable
)

func (k FilesystemKind) String() string {
	switch k {
	case FilesystemReliable:
		return "reliable"
	case FilesystemUnreliable:
		return "unreliable"
	default:
		return "unknown"
	}
}

// Status is the per-folder watcher-health snapshot that reconcile
// consults to decide whether to emit a RescanFolderNow action.
type Status struct {
	// Path is the absolute filesystem path the status describes.
	Path string

	// FilesystemType is the human-readable filesystem name as
	// reported by the OS (e.g. "ext4", "apfs", "NTFS", "nfs"). Used
	// for log messages and CHANGELOG-quotable evidence.
	FilesystemType string

	// Kind is the trust classification derived from FilesystemType.
	// Decisions in reconcile/diff branch on this rather than on the
	// string so the per-platform tables stay encapsulated.
	Kind FilesystemKind

	// OverflowSeen reports whether the watcher signalled an event
	// queue overflow for this folder since the last reconcile cycle
	// drained the flag. Linux: IN_Q_OVERFLOW. Windows:
	// ERROR_NOTIFY_ENUM_DIR. macOS: not surfaced — FSEvents
	// generally coalesces gracefully, but the field stays available
	// for future platform-specific signals.
	OverflowSeen bool

	// WatchLimitHit reports that adding watches failed because of a
	// resource limit (Linux ENOSPC on inotify_add_watch, Windows
	// running out of handles, BSD fd-limit). Reconcile uses this to
	// keep periodic rescan enabled for the affected folder until
	// the limit is raised by the operator.
	WatchLimitHit bool

	// LastReliableEventAt is the most recent timestamp at which the
	// watcher delivered a "trustworthy" event for this folder. Used
	// as a heuristic: if the watcher hasn't said anything in a long
	// time and we don't trust the filesystem, we rescan to find
	// out whether the filesystem actually went quiet or whether
	// the watcher silently broke. Zero when no events seen yet.
	LastReliableEventAt time.Time
}

// Querier is the minimum surface reconcile needs from a health
// tracker. Returns the status for a folder root and an ok flag
// indicating whether the path is tracked at all. Kept narrow so the
// implementing package stays free to evolve internals (e.g. moving
// from in-memory to persisted state) without touching reconcile.
type Querier interface {
	Status(root string) (Status, bool)
}

// SleepEvent is the signal emitted by the wake detector when it
// suspects the host machine has resumed from sleep. Reconcile reacts
// by issuing a one-shot rescan of every folder, because the watcher
// quiesced during sleep and any out-of-band changes (USB drive
// swap, scheduled task on a peer that finished while the laptop
// was closed) need to be reconciled.
type SleepEvent struct {
	// DetectedAt is the wall-clock time at which the detector first
	// observed the gap.
	DetectedAt time.Time

	// Gap is the observed wall-clock jump that triggered the
	// detection. Useful in logs to distinguish "5-minute laptop
	// lid close" from "8-hour overnight suspend"; reconcile treats
	// them identically.
	Gap time.Duration
}

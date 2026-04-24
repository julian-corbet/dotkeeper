// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// StateV2 is the v0.5 schema for $XDG_STATE_HOME/dotkeeper/state.toml.
// This file is owned exclusively by dotkeeper — never hand-edited. It holds
// runtime identity, peer discoveries, and cached observation state (ADR 0002).
type StateV2 struct {
	SchemaVersion int `toml:"schema_version"`

	// SyncthingDeviceID is the Syncthing device ID derived from this machine's
	// Syncthing private key.
	SyncthingDeviceID string `toml:"syncthing_device_id"`

	// Peers is the list of known mesh peers discovered via dotkeeper pair.
	Peers []PeerEntry `toml:"peers"`

	// TrackedOverrides lists absolute paths to repos outside any scan root
	// that have been explicitly registered via dotkeeper track.
	TrackedOverrides []string `toml:"tracked_overrides"`

	// ObservedRepos maps absolute repo paths to their last-observed state.
	// The reconciler writes here after each successful pass.
	ObservedRepos map[string]ObservedRepo `toml:"observed_repos"`

	// LastSeenPeers maps Syncthing device IDs to the last time they were seen.
	LastSeenPeers map[string]time.Time `toml:"last_seen_peers"`
}

// PeerEntry records a mesh peer's identity as learned during pairing.
type PeerEntry struct {
	Name      string    `toml:"name"`
	DeviceID  string    `toml:"device_id"`
	LearnedAt time.Time `toml:"learned_at"`
}

// ObservedRepo holds the last-known git and backup state for a tracked repo.
type ObservedRepo struct {
	// LastReconciledCommit is the commit hash at the time of the last
	// successful reconcile pass.
	LastReconciledCommit string `toml:"last_reconciled_commit"`
	// LastPushedCommit is the commit hash of the last successful git push.
	LastPushedCommit string `toml:"last_pushed_commit"`
	// LastBackupAt is the timestamp of the last successful git backup.
	LastBackupAt time.Time `toml:"last_backup_at"`
}

// LoadStateV2 reads state.toml from the XDG state directory. Returns nil (no
// error) if the file does not yet exist. Nil maps/slices are initialised to
// empty values so callers can write to them without nil checks.
func LoadStateV2() (*StateV2, error) {
	path := StateV2Path()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var s StateV2
	if _, err := toml.DecodeFile(path, &s); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	if s.Peers == nil {
		s.Peers = []PeerEntry{}
	}
	if s.TrackedOverrides == nil {
		s.TrackedOverrides = []string{}
	}
	if s.ObservedRepos == nil {
		s.ObservedRepos = make(map[string]ObservedRepo)
	}
	if s.LastSeenPeers == nil {
		s.LastSeenPeers = make(map[string]time.Time)
	}
	return &s, nil
}

// WriteStateV2 writes s to state.toml. The state directory is created with
// mode 0700 if it does not exist. The file itself is written with mode 0600
// because it may contain sensitive identity information.
func WriteStateV2(s *StateV2) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# dotkeeper state (v2) — tool-owned, do not hand-edit\n\n")

	fmt.Fprintf(&b, "schema_version = %d\n", s.SchemaVersion)
	fmt.Fprintf(&b, "syncthing_device_id = %q\n", s.SyncthingDeviceID)

	b.WriteString("tracked_overrides = [\n")
	for _, p := range s.TrackedOverrides {
		fmt.Fprintf(&b, "    %q,\n", p)
	}
	b.WriteString("]\n")

	if len(s.Peers) > 0 {
		b.WriteString("\n")
		for _, p := range s.Peers {
			b.WriteString("[[peers]]\n")
			fmt.Fprintf(&b, "name = %q\n", p.Name)
			fmt.Fprintf(&b, "device_id = %q\n", p.DeviceID)
			fmt.Fprintf(&b, "learned_at = %s\n", p.LearnedAt.UTC().Format(time.RFC3339))
			b.WriteString("\n")
		}
	}

	for path, obs := range s.ObservedRepos {
		b.WriteString("[observed_repos")
		fmt.Fprintf(&b, ".%q]\n", sanitizeTOMLKey(path))
		fmt.Fprintf(&b, "last_reconciled_commit = %q\n", obs.LastReconciledCommit)
		fmt.Fprintf(&b, "last_pushed_commit = %q\n", obs.LastPushedCommit)
		if obs.LastBackupAt.IsZero() {
			fmt.Fprintf(&b, "last_backup_at = %s\n", time.Time{}.UTC().Format(time.RFC3339))
		} else {
			fmt.Fprintf(&b, "last_backup_at = %s\n", obs.LastBackupAt.UTC().Format(time.RFC3339))
		}
		b.WriteString("\n")
	}

	for deviceID, ts := range s.LastSeenPeers {
		b.WriteString("[last_seen_peers]\n")
		fmt.Fprintf(&b, "%q = %s\n", sanitizeTOMLKey(deviceID), ts.UTC().Format(time.RFC3339))
	}

	return os.WriteFile(StateV2Path(), []byte(b.String()), 0o600)
}

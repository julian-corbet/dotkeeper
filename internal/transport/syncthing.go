// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SyncthingClient is the narrow interface SyncthingTransport needs
// from internal/stclient. Defined here rather than imported as a
// type alias because we want the transport package's dependency
// graph to be "everything I need from outside is an interface" —
// makes the package trivially testable with stubs and prevents
// reverse-dependency creep.
type SyncthingClient interface {
	GetConfig() (map[string]any, error)
	SetConfig(cfg map[string]any) error
	AddDevice(deviceID, name string) error
	Ping() error
}

// SyncthingTransport is the dotkeeper-default transport: peers
// receive changes via the embedded Syncthing instance's BEP gossip.
// All "make this peer receive changes" operations boil down to
// "ensure the peer is in the Syncthing folder's device list";
// propagation itself is invisible — Syncthing handles it without
// any per-change call from dotkeeper.
//
// This implementation always reports Available()=true on every
// platform dotkeeper supports, because the embedded Syncthing is
// part of the dotkeeper binary itself. There is no "Syncthing not
// installed" failure mode; if dotkeeper is running, the transport
// is available.
type SyncthingTransport struct {
	ST SyncthingClient
}

// NewSyncthingTransport wires the transport to a Syncthing API
// client. A nil client is permitted at construction time (the
// daemon may start before Syncthing's API has bound its port);
// methods return an error rather than panicking when the client
// is unavailable.
func NewSyncthingTransport(st SyncthingClient) *SyncthingTransport {
	return &SyncthingTransport{ST: st}
}

// Name implements Transport.Name.
func (s *SyncthingTransport) Name() string { return "syncthing" }

// Available implements Transport.Available. Always true — the
// embedded Syncthing is built into dotkeeper itself; there is no
// "missing prerequisite" scenario for this transport.
func (s *SyncthingTransport) Available() bool { return true }

// EnsurePeerReachability adds peer.DeviceID to folder.ID's device
// list in Syncthing's running config. Idempotent: when the peer
// is already in the list, returns nil without mutating config.
//
// The current implementation reads the entire config, mutates the
// one folder, and writes it back — same pattern as the
// stclient.Client.AddOrUpdateFolder path. Future refinement
// (v1.0.0+) may switch to PATCH-style endpoints if Syncthing's
// REST API exposes them for folder devices.
func (s *SyncthingTransport) EnsurePeerReachability(_ context.Context, folder Folder, peer Peer) error {
	if s.ST == nil {
		return errors.New("SyncthingTransport: client not available (is Syncthing running?)")
	}
	if peer.DeviceID == "" {
		return fmt.Errorf("SyncthingTransport.EnsurePeerReachability: peer %q has empty DeviceID", peer.Name)
	}
	cfg, err := s.ST.GetConfig()
	if err != nil {
		return fmt.Errorf("SyncthingTransport.EnsurePeerReachability: get config: %w", err)
	}
	folders, _ := cfg["folders"].([]any)
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] != folder.ID {
			continue
		}
		devices, _ := fm["devices"].([]any)
		for _, d := range devices {
			dm, _ := d.(map[string]any)
			if dm["deviceID"] == peer.DeviceID {
				// Already present — true idempotent return,
				// no config write needed.
				return nil
			}
		}
		devices = append(devices, map[string]any{
			"deviceID":     peer.DeviceID,
			"introducedBy": "",
		})
		fm["devices"] = devices
		folders[i] = fm
		cfg["folders"] = folders
		return s.ST.SetConfig(cfg)
	}
	return fmt.Errorf("SyncthingTransport.EnsurePeerReachability: folder %q not found in Syncthing config", folder.ID)
}

// RemovePeerReachability strips peer.DeviceID from folder.ID's
// device list. Idempotent: returns nil when the peer is already
// absent from the list. A folder-not-found error is still an error
// because the caller expects the folder to exist; only the
// peer-membership operation is idempotent.
func (s *SyncthingTransport) RemovePeerReachability(_ context.Context, folder Folder, peer Peer) error {
	if s.ST == nil {
		return errors.New("SyncthingTransport: client not available")
	}
	if peer.DeviceID == "" {
		return nil // nothing to remove
	}
	cfg, err := s.ST.GetConfig()
	if err != nil {
		return fmt.Errorf("SyncthingTransport.RemovePeerReachability: get config: %w", err)
	}
	folders, _ := cfg["folders"].([]any)
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] != folder.ID {
			continue
		}
		devices, _ := fm["devices"].([]any)
		filtered := devices[:0]
		removed := false
		for _, d := range devices {
			dm, _ := d.(map[string]any)
			if dm["deviceID"] == peer.DeviceID {
				removed = true
				continue
			}
			filtered = append(filtered, d)
		}
		if !removed {
			return nil // already absent
		}
		fm["devices"] = filtered
		folders[i] = fm
		cfg["folders"] = folders
		return s.ST.SetConfig(cfg)
	}
	return fmt.Errorf("SyncthingTransport.RemovePeerReachability: folder %q not found", folder.ID)
}

// Probe pings the local Syncthing API and reports the round-trip.
// This measures the dotkeeper-to-Syncthing path, not the full
// dotkeeper-to-peer path — Syncthing doesn't expose a per-peer
// latency probe via REST. The result is still useful as the
// transport's "is this transport usable" gate and as a baseline
// for relative ranking against other transports; absolute
// peer-to-peer latency under BEP is dominated by Syncthing's gossip
// scheduling, not by the network RTT.
//
// Returns ErrUnreachable when the API client is unset (Syncthing
// not yet up) — distinct from transient errors so the manager can
// treat it as a "skip until next re-probe" signal.
func (s *SyncthingTransport) Probe(_ context.Context, _ Peer) (time.Duration, error) {
	if s.ST == nil {
		return 0, ErrUnreachable
	}
	start := time.Now()
	if err := s.ST.Ping(); err != nil {
		return 0, fmt.Errorf("SyncthingTransport.Probe: %w", err)
	}
	return time.Since(start), nil
}

// PropagateChange is a no-op for SyncthingTransport: BEP gossip
// handles the actual propagation invisibly. Including it in the
// interface anyway lets active transports (GitSSHTransport in
// v1.0.0) be drop-in compatible with reconcile's existing
// commit-driven loop.
func (s *SyncthingTransport) PropagateChange(_ context.Context, _ Change, _ Peer) error {
	return nil
}

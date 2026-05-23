// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// presenceQuerier is the narrow seam the peer-presence tracker
// needs from stclient. Tests inject a stub that returns a
// hand-built *stclient.Connections — no need to spin up real
// Syncthing.
type presenceQuerier interface {
	GetConnections() (*stclient.Connections, error)
}

// runPeerPresenceTracker periodically asks Syncthing which peers
// are currently connected and persists those observations to
// state.LastSeenPeers. The health command consults the same map
// as a fallback when Syncthing's live API is unreachable; without
// this tracker, state.LastSeenPeers stays empty forever and
// health renders connected peers as "never seen" on every install
// where the daemon hasn't been freshly started.
//
// Design notes:
//
//   - Runs in its own goroutine on the same tick as reconcile.
//     Independent timing so a slow API call can't stall reconcile
//     and a reconcile failure can't drop a presence update.
//   - First update fires immediately so the cache is populated
//     before the first health query rather than after the first
//     full reconcile-interval delay.
//   - GetConnections + MutateStateV2 failures log at DEBUG / WARN
//     respectively and do NOT crash. The tracker is observability,
//     not load-bearing state.
//   - Honours ctx.Done so daemon shutdown cleans up the goroutine
//     without leaking.
func runPeerPresenceTracker(ctx context.Context, st presenceQuerier, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	update := func() {
		conns, err := st.GetConnections()
		if err != nil {
			logger.DebugContext(ctx, "peer-presence: GetConnections failed", "err", err)
			return
		}
		now := time.Now()
		err = config.MutateStateV2(func(s *config.StateV2) error {
			if s.LastSeenPeers == nil {
				s.LastSeenPeers = make(map[string]time.Time)
			}
			for deviceID, conn := range conns.Connections {
				if conn.Connected {
					s.LastSeenPeers[deviceID] = now
				}
			}
			return nil
		})
		if err != nil {
			logger.WarnContext(ctx, "peer-presence: MutateStateV2 failed", "err", err)
		}
	}

	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			update()
		}
	}
}

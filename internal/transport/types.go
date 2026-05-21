// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package transport abstracts the mechanism by which managed folders'
// changes propagate between peers. The interface is the foundation
// for v1.0.0's intelligent transport selection: today's dotkeeper has
// exactly one implementation (SyncthingTransport, wrapping the
// embedded Syncthing instance); tomorrow's will gain a
// GitSSHTransport, a TransportManager that probes both for each
// peer, and a microbenchmark-driven pick of the fastest reachable
// path per peer.
//
// Boundary of "transport" in this package:
//
//   - In scope: configuring a peer to receive changes ("add to the
//     folder's device list", "git remote add"), measuring reachability
//     and latency to a peer, actively propagating a known change
//     (a no-op for Syncthing — its BEP gossip handles it — but a
//     real `git push` for GitSSH).
//
//   - Out of scope: local Syncthing folder management (pause/unpause,
//     schedule rewrites, manual rescans). Those are local-daemon
//     operations and stay in internal/stclient. The conceptual line:
//     anything that talks to the *peer* is transport; anything that
//     talks to the *local Syncthing* is stclient.
package transport

import (
	"context"
	"errors"
	"time"
)

// Folder is the dotkeeper-side view of a managed folder for transport
// purposes. The fields below are the minimum needed to ensure peer
// reachability and propagate changes; richer metadata (devices, scan
// state, ignore patterns) belongs to the reconcile types, not here.
type Folder struct {
	// ID is the Syncthing folder ID for SyncthingTransport, or the
	// remote-name suffix for GitSSHTransport. Distinct from human
	// names; carried through to the transport implementation.
	ID string

	// Path is the absolute filesystem path of the folder root on
	// this host. Needed by GitSSHTransport to invoke git in the
	// right directory.
	Path string
}

// Peer identifies a remote dotkeeper instance. The fields below are
// the union of what any transport implementation needs; individual
// transports may use a subset:
//
//   - SyncthingTransport uses DeviceID.
//   - GitSSHTransport uses Hostname (resolved via Tailscale/mDNS/
//     static-config depending on the transport variant) and optional
//     User.
//
// A single peer entity is represented by one Peer value even when it's
// reachable via multiple transports; the TransportManager pairs the
// Peer with each available Transport when probing.
type Peer struct {
	// Name is the human-readable label ("CACHYOS-Elitebook"). Used
	// in logs and CLI output. Never used as a transport address.
	Name string

	// DeviceID is the Syncthing device ID. Empty for peers known
	// only via non-Syncthing transports.
	DeviceID string

	// Hostname is the network-reachable hostname or address used by
	// GitSSH-class transports. Resolution path depends on the
	// transport variant: Tailscale's MagicDNS for tailscale-variants,
	// `<name>.local` for mDNS-variants, the static-config value for
	// configured-static-variants.
	Hostname string

	// User is the SSH login user. Empty defaults to the local user
	// at SSH invocation time (`ssh hostname` rather than
	// `ssh user@hostname`).
	User string
}

// Change identifies what propagated. For now it's the bare minimum
// (folder + commit ref or sync-event marker); v1.0.0 may extend with
// payload-size hints when those inform realistic-payload probing.
type Change struct {
	Folder Folder

	// CommitHash, when non-empty, is the git commit hash that
	// represents the change in dotkeeper's canonical state. Used by
	// GitSSHTransport to construct the actual push.
	CommitHash string
}

// ErrUnreachable is returned by Probe when the transport cannot
// currently reach the peer (network down, host unresolvable, port
// refused). Distinct from a transient error so callers can implement
// "remove this transport from the candidate list until next re-probe"
// without surfacing it as a user-visible failure.
var ErrUnreachable = errors.New("peer unreachable via this transport")

// Transport is the dotkeeper-side abstraction over peer-change
// propagation. Every implementation must be safe for concurrent use:
// the v1.0.0 TransportManager probes and propagates across multiple
// goroutines.
//
// Implementations must be cheap to construct. The expensive setup
// (Syncthing folder configuration, SSH connection warm-up) happens
// inside EnsurePeerReachability so failures land at a defined point
// in the reconcile cycle rather than during package init.
type Transport interface {
	// Name returns a stable identifier for logs and CLI output:
	// "syncthing" for the embedded Syncthing transport,
	// "git-ssh+tailscale" / "git-ssh+mdns" / "git-ssh+static" for
	// the v1.0.0 GitSSH variants. The "+tag" suffix on GitSSH
	// distinguishes how peer hostnames were resolved without
	// requiring callers to inspect Peer.Hostname.
	Name() string

	// Available reports whether this transport's prerequisites are
	// satisfied on the local host. Cheap; called once per
	// TransportManager startup and on network-change events.
	// Returns false when, e.g., `tailscale` is not installed for the
	// Tailscale variant. Implementations must not block on
	// network I/O.
	Available() bool

	// EnsurePeerReachability creates whatever per-peer
	// configuration this transport needs so future changes to
	// folder can reach peer. Idempotent: calling it twice with
	// the same arguments has the same observable effect as calling
	// it once. ctx is honoured so reconcile can bound the call's
	// blocking time.
	//
	// For SyncthingTransport: adds peer.DeviceID to the folder's
	// device list (and ensures the device itself is registered).
	// For GitSSHTransport (v1.0.0): runs `git remote add` so the
	// local repo knows about the peer's git endpoint.
	EnsurePeerReachability(ctx context.Context, folder Folder, peer Peer) error

	// RemovePeerReachability is the inverse — used when a peer is
	// unregistered or moved to a different transport instance.
	// Idempotent: removing a peer that was never added is a no-op.
	RemovePeerReachability(ctx context.Context, folder Folder, peer Peer) error

	// Probe measures the round-trip time for a small payload to
	// the peer. Returns ErrUnreachable when the peer cannot be
	// contacted via this transport. Other errors indicate
	// transient failures (DNS hiccup, momentary network glitch);
	// callers should treat them as "skip this transport for this
	// cycle and re-probe next time" rather than as evidence the
	// transport is broken.
	//
	// Used by TransportManager to rank transports per peer.
	// Implementations should keep the probe cheap and bounded
	// (the v1.0.0 manager calls it on a 5-minute cycle multiplied
	// by every transport × every peer).
	Probe(ctx context.Context, peer Peer) (time.Duration, error)

	// PropagateChange actively delivers change to peer. A no-op for
	// transports whose backing system handles propagation
	// transparently (SyncthingTransport — Syncthing's BEP gossip
	// handles it). Mandatory for active transports like
	// GitSSHTransport, where dotkeeper must run an explicit
	// `git push` after each commit.
	//
	// Idempotent: re-propagating a change that has already arrived
	// at the peer must succeed without harm (git push of an
	// already-present commit is a no-op fast-forward; Syncthing's
	// no-op is genuinely free).
	PropagateChange(ctx context.Context, change Change, peer Peer) error
}

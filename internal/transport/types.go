// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package transport abstracts the mechanism by which managed folders'
// changes propagate between peers. dotkeeper ships two
// implementations:
//
//   - SyncthingTransport wraps the embedded Syncthing instance.
//     Always available; PropagateChange is a no-op because
//     Syncthing's BEP gossip handles propagation transparently.
//
//   - GitSSHTransport runs `git push` over SSH against the peer's
//     working tree. Available iff a Resolver (Tailscale ships
//     in v1.0; mDNS and static-hub are planned) can map the peer
//     to an SSH-reachable address.
//
// Manager owns both and routes per change via a cost model that
// learns from observed transfer durations.
//
// Boundary of "transport" in this package:
//
//   - In scope: configuring a peer to receive changes ("add to the
//     folder's device list", "git remote add"), measuring reachability
//     and latency to a peer, actively propagating a known change.
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
	// Name is the human-readable label (e.g. "laptop",
	// "desktop"). Used in logs and CLI output. Never used as a
	// transport address.
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

// Change identifies what propagated. Carries enough context that
// the Manager's Route(change, peer) call can pick the optimal
// transport based on the change's characteristics — primarily
// payload size, secondarily a coarse classification.
type Change struct {
	Folder Folder

	// CommitHash, when non-empty, is the git commit hash that
	// represents the change in dotkeeper's canonical state. Used by
	// GitSSHTransport to construct the actual push.
	CommitHash string

	// SizeHint is the approximate payload size in bytes that the
	// transport will need to move. Used by Manager.Route to
	// choose between transports — small payloads prefer
	// fast-setup transports (git+ssh), large payloads prefer
	// fast-throughput ones (Syncthing block-level transfer).
	// Zero means "unknown"; with the v1.0 priors, the cost model
	// at size=0 reduces to the setup cost alone, so the
	// fastest-setup transport wins (typically git-ssh). Callers
	// that don't actually know the size should leave this at
	// zero rather than guessing — incorrect size hints feed
	// the cost model's regression with noise.
	SizeHint int64

	// Kind classifies the payload semantically. Currently
	// informational — reserved for future routing policies that
	// branch on classification (text vs binary, code vs media)
	// in addition to size. Optional; zero value is fine.
	Kind ChangeKind
}

// ChangeKind classifies a payload's nature for routing purposes.
// v1.0.0 routing uses SizeHint only; Kind is recorded for future
// policies and for the CLI's output.
type ChangeKind int

const (
	// ChangeKindUnknown is the zero value — used when the caller
	// has no information about the change's nature.
	ChangeKindUnknown ChangeKind = iota
	// ChangeKindText is a text-mode change (source, config,
	// markdown). Typically small + well-compressed.
	ChangeKindText
	// ChangeKindBinary is a binary blob (image, archive, model
	// weights). Typically large + already compressed; benefits
	// from streaming transfer rather than line-by-line diff.
	ChangeKindBinary
)

func (k ChangeKind) String() string {
	switch k {
	case ChangeKindText:
		return "text"
	case ChangeKindBinary:
		return "binary"
	default:
		return "unknown"
	}
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
	// Used by Manager to rank transports per peer. Manager only
	// invokes Probe on discovery events (startup, wake from
	// suspend, explicit rediscover) — never on a periodic timer
	// — but implementations should still keep the probe cheap and
	// bounded so a wake event doesn't stall behind a slow
	// transport.
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

	// PropagatesSynchronously reports whether PropagateChange runs
	// the actual transfer inline and returns a duration that
	// reflects real work. True for transports that block until the
	// peer has acknowledged (GitSSHTransport: `git push` returns
	// after the remote ACKs). False for transports whose work
	// happens asynchronously in a background system
	// (SyncthingTransport: PropagateChange returns in microseconds
	// because BEP gossip in the embedded daemon does the actual
	// propagation later).
	//
	// Callers that feed observed-duration samples into a cost model
	// must skip the update for transports that return false here —
	// the ~µs no-op duration would otherwise teach the model that
	// the transport is infinitely fast and bias every future
	// routing decision toward it.
	PropagatesSynchronously() bool
}

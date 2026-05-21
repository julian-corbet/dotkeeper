// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
)

// Resolver maps a dotkeeper Peer to the network address GitSSHTransport
// should use when contacting that peer. Different resolvers represent
// different ways of discovering the address:
//
//   - TailscaleResolver: `tailscale status --json` lookup by hostname
//   - MDNSResolver: `<peer-name>.local` DNS lookup (planned)
//   - StaticResolver: user-configured address per peer (planned)
//
// A nil Resolver implementation is treated as "this resolution path is
// not available right now" — TransportManager skips the corresponding
// transport rather than failing the whole probe cycle.
type Resolver interface {
	// Name identifies the resolver in logs and CLI output:
	// "tailscale", "mdns", "static". The same string is appended to
	// GitSSHTransport.Name() as a suffix ("git-ssh+tailscale") so
	// operators can tell which resolution path was used without
	// inspecting internal state.
	Name() string

	// Available reports whether this resolver's prerequisites are
	// satisfied. Cheap; called at TransportManager startup. Returns
	// false when, e.g., the `tailscale` binary is absent from PATH.
	Available() bool

	// Resolve returns the network address (typically "host:port" or
	// "host" with an implied default port) for the given peer.
	// Returns ErrPeerUnknown when this resolver has no entry for
	// the peer; ErrResolverUnavailable when the resolver itself is
	// not currently usable (network down, daemon not running).
	// Other errors indicate transient failure and callers should
	// retry on the next probe cycle.
	Resolve(ctx context.Context, peer Peer) (address string, err error)
}

// ErrPeerUnknown indicates the resolver has no entry for the peer.
// TransportManager treats this as "skip this transport for this peer"
// — not a transient failure to retry, not a hard error to surface to
// the user. Most peers won't be reachable via every resolver; that's
// expected.
var ErrPeerUnknown = errors.New("peer not known to this resolver")

// ErrResolverUnavailable indicates the resolver's backing system is
// temporarily unusable. Distinct from ErrPeerUnknown so callers can
// log differently: "tailscale daemon not running" is operator info,
// "peer X not in tailnet" is fleet topology.
var ErrResolverUnavailable = errors.New("resolver unavailable")

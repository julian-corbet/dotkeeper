// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TailscaleResolver resolves peer names to TailscaleIPs by parsing
// `tailscale status --json`. Caches the result for cacheTTL between
// calls because the underlying CLI invocation is non-trivial
// (~30-100ms typically) and we don't want every Probe cycle to
// re-fork tailscale.
//
// Cross-platform: Tailscale ships official binaries for Linux,
// macOS, Windows, BSDs, and Solaris. The binary name is "tailscale"
// on every supported platform. We don't try to locate the daemon's
// IPC socket directly because the CLI handles the platform-specific
// IPC details for us and absorbs API changes between Tailscale
// versions.
type TailscaleResolver struct {
	// tailscaleBinary is the path or name of the tailscale CLI.
	// Defaults to "tailscale" (from PATH) but overridable for tests.
	tailscaleBinary string

	// cacheTTL bounds how often we re-invoke `tailscale status`.
	// 30s is the sweet spot: long enough that probe-driven
	// resolution is cheap, short enough that a peer appearing or
	// disappearing from the tailnet shows up within half a minute.
	cacheTTL time.Duration

	mu         sync.Mutex
	cached     map[string]string // hostname -> tailscale IP
	cachedAt   time.Time
	cachedErr  error // sticky error from the last fetch, if any
	unavailable bool // set when the CLI is provably absent
}

// NewTailscaleResolver returns a resolver with default settings:
// "tailscale" binary from PATH, 30-second cache TTL.
func NewTailscaleResolver() *TailscaleResolver {
	return &TailscaleResolver{
		tailscaleBinary: "tailscale",
		cacheTTL:        30 * time.Second,
	}
}

// Name implements Resolver.Name.
func (r *TailscaleResolver) Name() string { return "tailscale" }

// Available implements Resolver.Available. Returns true when the
// `tailscale` binary is on PATH AND `tailscale status` succeeds.
// We avoid blocking on a full status call here — that's done at
// first Resolve. The cheap path here is just looking for the binary
// via exec.LookPath.
//
// A resolver that has previously seen the CLI return an
// authentication-required or daemon-not-running error keeps
// reporting Available()=true; the actual error surfaces at Resolve
// where it can be propagated. This is intentional: a transient
// "tailscaled restarted" hiccup shouldn't permanently disable the
// transport for the daemon's lifetime.
func (r *TailscaleResolver) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unavailable {
		return false
	}
	if _, err := exec.LookPath(r.tailscaleBinary); err != nil {
		r.unavailable = true
		return false
	}
	return true
}

// tailscaleStatus is the trimmed shape of `tailscale status --json`
// that we actually consume. The real output has dozens of fields per
// peer; we ignore everything we don't need so a Tailscale version
// bump that adds new fields doesn't break our parser.
type tailscaleStatus struct {
	Self struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	Peer map[string]struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	} `json:"Peer"`
}

// Resolve implements Resolver.Resolve. Looks up the peer's hostname
// in the cached tailscale status; refreshes the cache if it's older
// than cacheTTL. Returns ErrPeerUnknown when the peer isn't in the
// tailnet at all.
//
// Hostname matching is case-insensitive because Tailscale lowercases
// hostnames in its API output while dotkeeper machine names can be
// mixed-case (e.g. "CACHYOS-Elitebook"). Matching against either
// Peer.Name or Peer.Hostname catches both conventions.
func (r *TailscaleResolver) Resolve(ctx context.Context, peer Peer) (string, error) {
	if err := r.refreshIfStale(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cachedErr != nil {
		return "", r.cachedErr
	}
	// Try matching by Hostname first (explicit), then by Name
	// (fallback for peers paired before the hostname was filled in).
	for _, candidate := range []string{peer.Hostname, peer.Name} {
		if candidate == "" {
			continue
		}
		lc := strings.ToLower(candidate)
		if ip, ok := r.cached[lc]; ok {
			return ip, nil
		}
	}
	return "", ErrPeerUnknown
}

func (r *TailscaleResolver) refreshIfStale(ctx context.Context) error {
	r.mu.Lock()
	stale := time.Since(r.cachedAt) > r.cacheTTL
	r.mu.Unlock()
	if !stale {
		return nil
	}
	return r.refresh(ctx)
}

func (r *TailscaleResolver) refresh(ctx context.Context) error {
	// Bound the fork+CLI invocation so a hung tailscaled doesn't
	// freeze the probe loop. 3 seconds is generous for the
	// `tailscale status --json` call on every supported platform.
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, r.tailscaleBinary, "status", "--json")
	out, err := cmd.Output()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.cachedAt = time.Now()
	if err != nil {
		// Distinguish "binary went missing mid-run" from "daemon not
		// running" so logs are useful. We treat both as
		// ErrResolverUnavailable since the resolver can't do its
		// job, but the sticky error preserves the underlying reason.
		r.cachedErr = fmt.Errorf("%w: tailscale status: %v", ErrResolverUnavailable, err)
		r.cached = nil
		return r.cachedErr
	}
	r.cachedErr = nil

	var st tailscaleStatus
	if err := json.Unmarshal(out, &st); err != nil {
		r.cachedErr = fmt.Errorf("%w: parse tailscale status JSON: %v", ErrResolverUnavailable, err)
		r.cached = nil
		return r.cachedErr
	}

	cached := make(map[string]string, len(st.Peer)+1)
	if st.Self.HostName != "" && len(st.Self.TailscaleIPs) > 0 {
		cached[strings.ToLower(st.Self.HostName)] = st.Self.TailscaleIPs[0]
	}
	for _, p := range st.Peer {
		// We intentionally do NOT skip offline peers here.
		// Offline peers are still in the tailnet but not
		// reachable right now — we cache the IP anyway because
		// reachability is the Probe's job, not the resolver's.
		// The Probe will fail; the manager will mark the
		// transport unreachable for this peer; on the peer
		// coming back online a subsequent Discover picks them
		// up.
		if p.HostName == "" || len(p.TailscaleIPs) == 0 {
			continue
		}
		cached[strings.ToLower(p.HostName)] = p.TailscaleIPs[0]
	}
	r.cached = cached
	return nil
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitSSHTransport carries dotkeeper-managed commits to a peer via
// `git push` over SSH. The peer-side endpoint is a bare repository
// at a path the operator has provisioned ahead of time (typically
// `~/.local/share/dotkeeper/repos/<folder-id>.git` on the peer); see
// docs/gitssh-setup.md (v1.1+) for the auto-provisioning helper.
//
// Variants of this transport differ only in how they resolve
// peer-name → ssh-reachable address. Each variant is a separate
// concrete instance with its own Resolver. v1.0.0 ships the
// Tailscale variant; mDNS and static-hub variants are tracked for
// follow-up releases.
type GitSSHTransport struct {
	// resolver maps Peer to address. Its Name() is used as the
	// transport-name suffix ("git-ssh+tailscale").
	resolver Resolver

	// runner executes the actual git/ssh commands. Default is
	// exec.CommandContext; tests inject a stub.
	runner commandRunner

	// probeTimeout bounds each Probe call. SSH connection setup can
	// take a couple of seconds on a cold cache; we cap it so a
	// silent firewall drop doesn't stall the manager's probe loop.
	probeTimeout time.Duration

	// pushTimeout bounds each PropagateChange call. Git push of a
	// small commit over Tailscale-class latency is well under a
	// second; 30 seconds accommodates initial-push cases where
	// the peer's bare repo just got provisioned.
	pushTimeout time.Duration
}

// commandRunner is the test seam for exec.CommandContext. Hidden
// behind an interface so unit tests don't need to fork real
// processes (which would slow CI and add platform-specific
// dependencies on `ssh` and `git` being installed).
type commandRunner interface {
	// Run invokes name with args under ctx. Returns the combined
	// stdout+stderr and any error. Matches exec.Cmd.CombinedOutput
	// semantics closely enough that the default implementation is
	// a one-line wrapper.
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// NewGitSSHTransport constructs a GitSSHTransport that uses the
// given resolver to map peers to addresses. resolver must be
// non-nil; pass a no-op resolver if you want to disable the
// transport.
func NewGitSSHTransport(resolver Resolver) *GitSSHTransport {
	return &GitSSHTransport{
		resolver:     resolver,
		runner:       execRunner{},
		probeTimeout: 5 * time.Second,
		pushTimeout:  30 * time.Second,
	}
}

// Name implements Transport.Name. Formatted as "git-ssh+<resolver>"
// so the resolution path is visible to operators: a CLI showing
// "git-ssh+tailscale (4ms)" is more useful than "git-ssh (4ms)"
// because it answers "how did dotkeeper find this peer" without
// requiring further investigation.
func (g *GitSSHTransport) Name() string {
	return "git-ssh+" + g.resolver.Name()
}

// Available implements Transport.Available. Delegates to the
// resolver — without a working resolver we can't find peers, so
// the transport is effectively unusable.
func (g *GitSSHTransport) Available() bool {
	return g.resolver.Available()
}

// EnsurePeerReachability registers the peer as a git remote in the
// folder's working tree. The remote name is composed from the
// dotkeeper folder ID and the resolver name, so each
// (folder, resolver) pair gets a unique remote and updates from
// different resolvers (Tailscale-resolved vs static-configured)
// don't clobber each other.
//
// Idempotent: a `git remote set-url` overwrites any existing URL
// for the same remote name; if the remote doesn't exist,
// `git remote add` creates it. We try set-url first because it's
// the common case (remote already exists from a prior reconcile);
// fall back to add on its specific failure.
func (g *GitSSHTransport) EnsurePeerReachability(ctx context.Context, folder Folder, peer Peer) error {
	addr, err := g.resolver.Resolve(ctx, peer)
	if err != nil {
		return fmt.Errorf("GitSSHTransport.EnsurePeerReachability: resolve %q: %w", peer.Name, err)
	}
	remoteName := g.remoteName(peer)
	remoteURL := g.remoteURL(addr, peer, folder)

	// Try set-url first — works when the remote already exists,
	// failing with a recognisable error when it doesn't.
	out, err := g.runner.Run(ctx, folder.Path, "git", "remote", "set-url", remoteName, remoteURL)
	if err == nil {
		return nil
	}
	// Heuristic: git's "No such remote" message is stable enough
	// across versions that we can branch on it. Falling back
	// unconditionally on any set-url failure would mask real
	// errors (permission denied, repo missing).
	if !strings.Contains(strings.ToLower(string(out)), "no such remote") &&
		!strings.Contains(strings.ToLower(string(out)), "no such remote ref") {
		return fmt.Errorf("GitSSHTransport.EnsurePeerReachability: set-url %s: %w (%s)", remoteName, err, strings.TrimSpace(string(out)))
	}
	out, err = g.runner.Run(ctx, folder.Path, "git", "remote", "add", remoteName, remoteURL)
	if err != nil {
		return fmt.Errorf("GitSSHTransport.EnsurePeerReachability: add %s: %w (%s)", remoteName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemovePeerReachability strips the per-peer git remote. Idempotent:
// "remote not found" is treated as success since the post-condition
// is "this remote does not exist" — already-true is fine.
func (g *GitSSHTransport) RemovePeerReachability(ctx context.Context, folder Folder, peer Peer) error {
	remoteName := g.remoteName(peer)
	out, err := g.runner.Run(ctx, folder.Path, "git", "remote", "remove", remoteName)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "no such remote") {
		return nil
	}
	return fmt.Errorf("GitSSHTransport.RemovePeerReachability: remove %s: %w (%s)", remoteName, err, strings.TrimSpace(string(out)))
}

// Probe measures round-trip time to the peer via `ssh peer true`.
// The shell builtin `true` is a no-op that returns immediately on
// the remote side; the RTT we measure is dominated by SSH
// handshake + one network round-trip. SSH connection multiplexing
// (ControlMaster) is honoured automatically when the user's SSH
// config enables it, which means subsequent probes hit a warm
// channel and report sub-handshake latency.
//
// Returns ErrUnreachable when the peer can't be resolved or SSH
// fails to connect. Distinguishes "resolver doesn't know this
// peer" (ErrPeerUnknown from the resolver, wrapped as
// ErrUnreachable) from "resolver knows but SSH fails" (also
// ErrUnreachable, with a wrapped error message naming the
// failure). The TransportManager treats them identically — skip
// this transport for this cycle — so the wrapper choice is for
// operator-facing diagnostics, not for control flow.
func (g *GitSSHTransport) Probe(ctx context.Context, peer Peer) (time.Duration, error) {
	if !g.Available() {
		return 0, ErrUnreachable
	}
	addr, err := g.resolver.Resolve(ctx, peer)
	if err != nil {
		// Wrap as ErrUnreachable so callers using errors.Is
		// get a single sentinel for "this transport+peer is
		// not currently usable."
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	pctx, cancel := context.WithTimeout(ctx, g.probeTimeout)
	defer cancel()

	target := addr
	if peer.User != "" {
		target = peer.User + "@" + addr
	}
	// -o BatchMode=yes prevents SSH from prompting (passwords,
	// keyboard-interactive); we want a clean machine-readable
	// success/failure, not a hung Probe.
	// -o ConnectTimeout=3 enforces a per-attempt deadline at the
	// SSH layer in addition to ctx; some SSH client builds
	// ignore ctx cancellation during the handshake.
	start := time.Now()
	out, err := g.runner.Run(pctx, "", "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=accept-new",
		target, "true")
	d := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("%w: ssh %s: %v (%s)", ErrUnreachable, target, err, strings.TrimSpace(string(out)))
	}
	return d, nil
}

// PropagateChange pushes change.CommitHash to the peer's git remote.
// The remote was added by an earlier EnsurePeerReachability call;
// if it doesn't exist, the push fails and the manager logs the
// error (recovery is for the operator to investigate why the
// remote went missing).
//
// Requires a bare repository at the resolved address. Setting up
// the peer-side bare is the operator's responsibility in v1.0.0;
// `dotkeeper bare-init` (v1.1+) will automate it. When the push
// fails because the bare doesn't exist, git's error message
// contains "repository ... not found" or similar, which the
// applier logs verbatim so the user knows what to fix.
//
// Idempotent: pushing a commit that already exists on the peer is
// a fast-forward no-op for git.
func (g *GitSSHTransport) PropagateChange(ctx context.Context, change Change, peer Peer) error {
	remoteName := g.remoteName(peer)
	pctx, cancel := context.WithTimeout(ctx, g.pushTimeout)
	defer cancel()

	args := []string{"push", remoteName}
	if change.CommitHash != "" {
		args = append(args, change.CommitHash+":refs/heads/main")
	}
	out, err := g.runner.Run(pctx, change.Folder.Path, "git", args...)
	if err != nil {
		// Surface git's stderr verbatim — "remote: error" lines
		// from the peer side often tell the operator exactly
		// what's wrong (missing bare repo, permission denied,
		// non-fast-forward).
		return fmt.Errorf("GitSSHTransport.PropagateChange: push to %s: %w: %s", remoteName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// remoteName composes the git remote name from the peer. Format:
// "dk+<resolver>+<peer-name>". The "dk+" prefix namespaces dotkeeper-
// managed remotes so we never collide with operator-added remotes
// (origin, upstream); the resolver suffix lets multiple variants
// (tailscale, mdns) coexist for the same peer without overlap.
//
// Peer names are sanitised: git remote names allow a fairly broad
// charset but not whitespace. We replace each non-alphanumeric with
// a hyphen to keep the result git-compatible. Two peers that
// disagree only on case still get distinct names because git
// remote names are case-sensitive.
func (g *GitSSHTransport) remoteName(peer Peer) string {
	sanitised := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, peer.Name)
	return "dk+" + g.resolver.Name() + "+" + sanitised
}

// remoteURL builds the SSH URL the git remote points at. Format:
// "ssh://[user@]host/~/.local/share/dotkeeper/repos/<folder-id>.git".
// The standardised path is the v1.0.0 convention for peer-side
// bare repos; documented and consumed by `dotkeeper bare-init`
// (v1.1+).
//
// Falls back to including the user explicitly when peer.User is
// set; otherwise SSH uses the local user from the operator's SSH
// config (the standard, less surprising default).
func (g *GitSSHTransport) remoteURL(addr string, peer Peer, folder Folder) string {
	host := addr
	if peer.User != "" {
		host = peer.User + "@" + addr
	}
	return "ssh://" + host + "/~/.local/share/dotkeeper/repos/" + folder.ID + ".git"
}

// ErrPushFailed wraps git push failures to make them programmatically
// identifiable. Reserved for v1.1+ when the manager may treat
// recoverable push errors (bare repo missing, peer offline)
// differently from unrecoverable ones (non-fast-forward).
var ErrPushFailed = errors.New("git push failed")

// _ verifies GitSSHTransport satisfies the Transport interface at
// compile time.
var _ Transport = (*GitSSHTransport)(nil)

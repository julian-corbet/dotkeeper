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
// `git push` over SSH. The peer-side endpoint is the peer's working
// tree at the same absolute path as on the local host (the v1.0.0
// mirror-paths convention, enforced by dotkeeper's `scan_roots`
// discovery). The peer-side repo must be configured with
// `receive.denyCurrentBranch=updateInstead` so a push to the
// currently-checked-out branch atomically updates the working tree
// — `dotkeeper bare-init` sets that config in one step.
//
// Variants of this transport differ only in how they resolve a
// peer's name to an SSH-reachable address. Each variant is a
// separate concrete instance with its own Resolver. v1.0.0 ships
// one resolver (Tailscale); the Resolver interface is the seam for
// mDNS and static-hub variants in follow-up releases.
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
	// small commit over typical low-latency SSH is well under a
	// second; 30 seconds accommodates initial-push cases where the
	// peer just had its first commit cycle and is materialising
	// the working tree from scratch.
	pushTimeout time.Duration
}

// CommandRunner is the test seam for exec.CommandContext. Exported
// in v1.0 so the daemon-propagator e2e tests can wrap the default
// exec runner with a URL-rewriter without standing up a real SSH
// server. Production callers should use NewGitSSHTransport (which
// uses the default runner); NewGitSSHTransportWithRunner exists
// purely as a test seam.
type CommandRunner interface {
	// Run invokes name with args under ctx. Returns the combined
	// stdout+stderr and any error. Matches exec.Cmd.CombinedOutput
	// semantics closely enough that the default implementation is
	// a one-line wrapper.
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// commandRunner is an internal alias kept for compatibility with
// the GitSSHTransport.runner field declaration. New code should
// reference CommandRunner.
type commandRunner = CommandRunner

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// NewExecRunner returns the default CommandRunner — a thin wrapper
// over exec.CommandContext + CombinedOutput. Exported so tests can
// layer wrappers (URL rewriters, latency injectors) without
// re-implementing the exec plumbing.
func NewExecRunner() CommandRunner { return execRunner{} }

// NewGitSSHTransport constructs a GitSSHTransport that uses the
// given resolver to map peers to addresses. resolver must be
// non-nil; pass a no-op resolver if you want to disable the
// transport.
func NewGitSSHTransport(resolver Resolver) *GitSSHTransport {
	return NewGitSSHTransportWithRunner(resolver, execRunner{})
}

// NewGitSSHTransportWithRunner is the test-seam constructor: same
// as NewGitSSHTransport but accepts a custom CommandRunner.
// Production callers should use NewGitSSHTransport; runner
// injection exists so e2e tests can substitute a URL-rewriter that
// points ssh:// at a local file:// destination.
func NewGitSSHTransportWithRunner(resolver Resolver, runner CommandRunner) *GitSSHTransport {
	return &GitSSHTransport{
		resolver:     resolver,
		runner:       runner,
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

// PropagatesSynchronously implements Transport. Always true:
// `git push` blocks until the remote ACKs receipt of the packfile,
// so the elapsed duration of PropagateChange reflects real work
// and is meaningful input to the cost model.
func (g *GitSSHTransport) PropagatesSynchronously() bool { return true }

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
	if !strings.Contains(strings.ToLower(string(out)), "no such remote") {
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

// PropagateChange pushes the change to the peer via git push over
// SSH. The remote was added by an earlier EnsurePeerReachability
// call; if it doesn't exist, the push fails and the manager logs
// the error (recovery is for the operator to investigate).
//
// Pushes against the peer's working tree directly using git's
// `receive.denyCurrentBranch=updateInstead` semantics — when the
// peer-side repo has that setting (provisioned via
// `dotkeeper bare-init`), a push to the currently-checked-out
// branch updates the peer's working tree on the spot. No separate
// bare repo, no post-receive hook, no second authoritative copy
// of the data.
//
// The refspec is `<src>:refs/heads/main` — v1.0 assumes the
// peer's checked-out branch is `main`, matching dotkeeper's
// scan-roots convention. Repos that use a different branch name
// need the planned per-repo branch override.
//
// Idempotent: pushing a commit that already exists on the peer is
// a fast-forward no-op for git.
func (g *GitSSHTransport) PropagateChange(ctx context.Context, change Change, peer Peer) error {
	remoteName := g.remoteName(peer)
	pctx, cancel := context.WithTimeout(ctx, g.pushTimeout)
	defer cancel()

	// Determine the source ref. If the caller supplied a commit hash,
	// push exactly that hash to the peer's current branch. Otherwise
	// push the local HEAD, which is the right default for the
	// "dotkeeper just committed something" flow.
	srcRef := "HEAD"
	if change.CommitHash != "" {
		srcRef = change.CommitHash
	}
	// We push to refs/heads/<branch> where <branch> is the same name
	// as the local checkout. Determining the local branch requires a
	// separate git invocation; for v1.0 we assume "main" because
	// dotkeeper-tracked repos default to that. Reading the branch
	// name from .dotkeeper.toml is a planned extension for repos
	// that diverge from the default.
	dstRef := "refs/heads/main"

	args := []string{"push", remoteName, srcRef + ":" + dstRef}
	out, err := g.runner.Run(pctx, change.Folder.Path, "git", args...)
	if err != nil {
		// Surface git's stderr verbatim. The most common failure
		// mode is "remote: error: refusing to update checked out
		// branch" — happens when the peer-side hasn't been
		// configured with denyCurrentBranch=updateInstead, which
		// is what `dotkeeper bare-init` fixes.
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

// remoteURL builds the SSH URL the git remote points at. v1.0.0
// targets the peer's WORKING TREE directly (not a separate bare
// repo) and relies on `receive.denyCurrentBranch=updateInstead` —
// provisioned via `dotkeeper bare-init` — so a push to the checked-
// out branch updates the working tree atomically.
//
// Path convention: peer's working tree lives at the same absolute
// path on the peer as on this machine. This is the v1.0.0
// constraint; the dotkeeper folder discovery enforces consistent
// paths under scan_roots already, so for the common case the
// constraint is invisible. A per-peer path map for users with
// diverging layouts is a planned extension.
//
// IPv6 addresses are bracketed (`ssh://[fe80::1]/path`) per
// RFC 3986. The Tailscale resolver typically returns IPv4 first,
// but operators using IPv6-only mesh setups still get a
// well-formed URL.
//
// Falls back to including the user explicitly when peer.User is
// set; otherwise SSH uses the local user from the operator's SSH
// config (the standard, less surprising default).
func (g *GitSSHTransport) remoteURL(addr string, peer Peer, folder Folder) string {
	host := addr
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
		host = "[" + addr + "]"
	}
	if peer.User != "" {
		host = peer.User + "@" + host
	}
	return "ssh://" + host + folder.Path
}

// ErrPushFailed wraps git push failures to make them
// programmatically identifiable. Reserved for follow-up work that
// distinguishes recoverable push errors (peer offline,
// updateInstead not configured) from unrecoverable ones
// (non-fast-forward).
var ErrPushFailed = errors.New("git push failed")

// _ verifies GitSSHTransport satisfies the Transport interface at
// compile time.
var _ Transport = (*GitSSHTransport)(nil)

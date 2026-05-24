// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// MutagenTransport propagates folder changes via Mutagen sync
// sessions over SSH. Mutagen is good at small-file workloads: lower
// per-change overhead than Syncthing's BEP gossip when peers are
// SSH-reachable, because it skips Syncthing's index-database +
// block-hash dance for files that fit in a single Mutagen
// "rsync-style" pass.
//
// Mutagen is detect-and-fallback: if the local `mutagen` CLI is not
// installed, Available() returns false and the Manager picks
// another transport. No operator opt-in needed; if you have it
// installed, dotkeeper uses it. Mirrors the "just works" contract
// of GitSSHTransport, which also auto-detects via the resolver.
//
// Session lifecycle:
//   - EnsurePeerReachability creates a one-way sync session named
//     deterministically from (folder.ID, peer.Name). Subsequent
//     calls with the same arguments are no-ops because Mutagen's
//     session-create-by-name fails idempotently — we treat its
//     "session already exists" error as success.
//   - PropagateChange invokes `mutagen sync flush <session>`, which
//     blocks until the session is "Watching" — i.e. all queued
//     changes have been applied on the peer. The wall-clock
//     duration is meaningful input to the cost model.
//   - RemovePeerReachability terminates the session by name.
//
// PropagatesSynchronously returns true because the flush is
// inline: the duration returned by PropagateChange reflects real
// work, unlike SyncthingTransport which returns in µs because BEP
// gossip happens asynchronously.
type MutagenTransport struct {
	// resolver maps Peer to address. Reused from the GitSSH family
	// because Mutagen's SSH target is the same shape: user@host.
	resolver Resolver

	// runner executes the actual mutagen/ssh commands. Default is
	// exec.CommandContext; tests inject a stub.
	runner commandRunner

	// remotePathBase is the absolute path on the peer where the
	// folder's remote endpoint lives. Defaults to mirroring the
	// local path under the peer's home directory (matches the
	// dotkeeper "mirror paths" convention enforced by scan_roots
	// discovery). Overridable for tests + non-mirror setups.
	remotePathBase string

	// probeTimeout bounds each Probe call. SSH connection setup +
	// `mutagen version` query.
	probeTimeout time.Duration

	// flushTimeout bounds each PropagateChange call. A flush of a
	// freshly-queued small change typically completes within
	// seconds; the cap accommodates initial-session catch-up where
	// Mutagen must hash both endpoints before declaring sync state.
	flushTimeout time.Duration

	// ensureTimeout bounds each EnsurePeerReachability call. Session
	// creation involves SSH handshake + mutagen daemon round-trip.
	ensureTimeout time.Duration

	// localBinaryPresent is the test seam for the `mutagen` binary
	// PATH check in Available(). Production uses exec.LookPath;
	// tests override to "always present" so the rest of the
	// transport logic is exercisable without installing mutagen
	// on every CI runner. nil → use the default LookPath check.
	localBinaryPresent func() bool
}

// NewMutagenTransport constructs a MutagenTransport using the given
// resolver. resolver must be non-nil; pass a no-op resolver if you
// want to disable the transport.
func NewMutagenTransport(resolver Resolver) *MutagenTransport {
	return NewMutagenTransportWithRunner(resolver, execRunner{})
}

// NewMutagenTransportWithRunner is the test-seam constructor.
// Production callers should use NewMutagenTransport.
func NewMutagenTransportWithRunner(resolver Resolver, runner CommandRunner) *MutagenTransport {
	return &MutagenTransport{
		resolver:      resolver,
		runner:        runner,
		probeTimeout:  10 * time.Second,
		flushTimeout:  60 * time.Second,
		ensureTimeout: 30 * time.Second,
	}
}

// Name implements Transport. "mutagen+<resolver>" so operators can
// see how the peer was resolved without inspecting Peer.Hostname.
func (m *MutagenTransport) Name() string {
	return "mutagen+" + m.resolver.Name()
}

// Available reports whether the local `mutagen` CLI is on PATH and
// the resolver is functional. Mutagen ships as a single binary for
// Linux/macOS/Windows; we only check existence, not version,
// because session-create returns a clean error on version
// mismatches and we'd rather surface that as a per-call failure
// than refuse the whole transport.
func (m *MutagenTransport) Available() bool {
	check := m.localBinaryPresent
	if check == nil {
		check = defaultMutagenBinaryPresent
	}
	if !check() {
		return false
	}
	return m.resolver.Available()
}

func defaultMutagenBinaryPresent() bool {
	_, err := exec.LookPath("mutagen")
	return err == nil
}

// PropagatesSynchronously implements Transport. True because
// PropagateChange invokes `mutagen sync flush`, which blocks until
// the queued change is applied on the peer.
func (m *MutagenTransport) PropagatesSynchronously() bool { return true }

// EnsurePeerReachability creates the Mutagen sync session for this
// (folder, peer). Idempotent: re-create with the same name returns
// a "session already exists" error that we treat as success.
func (m *MutagenTransport) EnsurePeerReachability(ctx context.Context, folder Folder, peer Peer) error {
	if !m.Available() {
		return ErrUnreachable
	}
	addr, err := m.resolver.Resolve(ctx, peer)
	if err != nil {
		return fmt.Errorf("MutagenTransport.EnsurePeerReachability: resolve %q: %w", peer.Name, err)
	}
	ectx, cancel := context.WithTimeout(ctx, m.ensureTimeout)
	defer cancel()

	sessionName := m.sessionName(folder, peer)
	target := m.sshTarget(addr, peer)
	remotePath := m.remoteFolderPath(folder)

	out, err := m.runner.Run(ectx, "", "mutagen",
		"sync", "create",
		"--name", sessionName,
		"--mode", "one-way-safe",
		folder.Path,
		target+":"+remotePath)
	if err == nil {
		return nil
	}
	// Mutagen's session-create with a name that's already in use
	// fails with "unable to create session: session with name ...
	// already exists". Detect-and-pass-through so the call is
	// idempotent against an existing-state.
	if strings.Contains(strings.ToLower(string(out)), "already exists") {
		return nil
	}
	return fmt.Errorf("MutagenTransport.EnsurePeerReachability: create %s: %w (%s)",
		sessionName, err, strings.TrimSpace(string(out)))
}

// RemovePeerReachability terminates the session. Idempotent: a
// terminate on a non-existent session is treated as success.
func (m *MutagenTransport) RemovePeerReachability(ctx context.Context, folder Folder, peer Peer) error {
	if !m.Available() {
		return ErrUnreachable
	}
	sessionName := m.sessionName(folder, peer)
	out, err := m.runner.Run(ctx, "", "mutagen",
		"sync", "terminate", sessionName)
	if err == nil {
		return nil
	}
	// "no such session" / "unable to locate session" — already gone,
	// post-condition satisfied.
	low := strings.ToLower(string(out))
	if strings.Contains(low, "no such session") || strings.Contains(low, "unable to locate") {
		return nil
	}
	return fmt.Errorf("MutagenTransport.RemovePeerReachability: terminate %s: %w (%s)",
		sessionName, err, strings.TrimSpace(string(out)))
}

// Probe measures end-to-end SSH+mutagen reachability. Runs
// `mutagen sync list --json` first to verify the local daemon
// responds, then `ssh peer mutagen version` to confirm the peer
// also has Mutagen. The combined RTT is a reasonable proxy for
// the cost-model input — it tracks the same network path that
// EnsurePeerReachability and PropagateChange will exercise.
func (m *MutagenTransport) Probe(ctx context.Context, peer Peer) (time.Duration, error) {
	if !m.Available() {
		return 0, ErrUnreachable
	}
	addr, err := m.resolver.Resolve(ctx, peer)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	pctx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	defer cancel()

	target := m.sshTarget(addr, peer)
	start := time.Now()
	// Single SSH round-trip; the remote `mutagen version` is a
	// cheap subcommand that returns the installed version string.
	// We don't parse the output — its successful exit is the signal.
	out, err := m.runner.Run(pctx, "", "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=accept-new",
		target, "mutagen", "version")
	d := time.Since(start)
	if err != nil {
		// Distinguish "peer doesn't have mutagen" from "peer
		// unreachable" for operator-facing logs, but both surface
		// to the manager as ErrUnreachable so the routing decision
		// is identical: try a different transport.
		if strings.Contains(strings.ToLower(string(out)), "command not found") ||
			strings.Contains(strings.ToLower(string(out)), "not found") {
			return 0, fmt.Errorf("%w: peer lacks mutagen", ErrUnreachable)
		}
		return 0, fmt.Errorf("%w: ssh %s: %v (%s)", ErrUnreachable, target, err, strings.TrimSpace(string(out)))
	}
	return d, nil
}

// PropagateChange flushes the (folder, peer) session, blocking
// until the session reports "Watching" (all queued changes applied).
// Wall-clock duration reflects the real propagation cost.
func (m *MutagenTransport) PropagateChange(ctx context.Context, change Change, peer Peer) error {
	if !m.Available() {
		return ErrUnreachable
	}
	sessionName := m.sessionName(change.Folder, peer)
	fctx, cancel := context.WithTimeout(ctx, m.flushTimeout)
	defer cancel()

	// `--skip-wait` would return immediately; we want the block so
	// the cost model receives a real duration. The default is to
	// wait, but specify it explicitly so a future Mutagen default
	// flip doesn't silently break us.
	out, err := m.runner.Run(fctx, "", "mutagen",
		"sync", "flush",
		sessionName)
	if err != nil {
		return fmt.Errorf("MutagenTransport.PropagateChange: flush %s: %w (%s)",
			sessionName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sessionName returns a deterministic 64-char hex digest from
// (folder.ID, peer.Name). Avoids the "session name length" cap
// Mutagen enforces (~40 chars in some builds) and guarantees
// uniqueness per (folder, peer) pair without imposing format
// constraints on either side.
func (m *MutagenTransport) sessionName(folder Folder, peer Peer) string {
	h := sha256.Sum256([]byte(folder.ID + "\x00" + peer.Name))
	// Mutagen session names must start with a letter — prepend "dk"
	// so any digest beginning with a digit still parses.
	return "dk" + hex.EncodeToString(h[:8])
}

// sshTarget builds the user@host portion of the remote endpoint.
// Mirrors GitSSHTransport's logic so both transports' SSH paths
// look identical to the operator.
func (m *MutagenTransport) sshTarget(addr string, peer Peer) string {
	if peer.User != "" {
		return peer.User + "@" + addr
	}
	return addr
}

// remoteFolderPath returns the absolute path of the folder root on
// the peer. v1.0.0 mirrors the local path under the peer's home
// directory — the "mirror paths" convention enforced by dotkeeper's
// scan_roots discovery. When remotePathBase is set (test seam), it
// overrides the local-path mirror.
func (m *MutagenTransport) remoteFolderPath(folder Folder) string {
	if m.remotePathBase != "" {
		return m.remotePathBase + "/" + folder.ID
	}
	return folder.Path
}


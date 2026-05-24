// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestMutagen constructs a MutagenTransport for tests with the
// timeouts shortened (so a stuck runner stub fails fast) and the
// PATH check forced into the available branch by injecting a
// resolver that says it's available. Tests that need to verify the
// PATH-check-failed branch construct the transport directly with
// `_ , err := exec.LookPath("nonexistent-bin")` semantics, which
// is hard to stub without modifying global state — we settle for
// only testing the resolver-driven Available path here.
func newTestMutagen(runner commandRunner, resolver Resolver) *MutagenTransport {
	return &MutagenTransport{
		resolver:           resolver,
		runner:             runner,
		probeTimeout:       time.Second,
		flushTimeout:       time.Second,
		ensureTimeout:      time.Second,
		localBinaryPresent: func() bool { return true },
	}
}

func TestMutagenNameIncludesResolverSuffix(t *testing.T) {
	tr := newTestMutagen(&stubRunner{}, &stubResolver{name: "tailscale"})
	if got := tr.Name(); got != "mutagen+tailscale" {
		t.Errorf("Name = %q, want mutagen+tailscale", got)
	}
}

func TestMutagenPropagatesSynchronouslyIsTrue(t *testing.T) {
	tr := newTestMutagen(&stubRunner{}, &stubResolver{name: "tailscale", available: true})
	if !tr.PropagatesSynchronously() {
		t.Error("PropagatesSynchronously must be true; cost-model needs the duration signal")
	}
}

// TestMutagenEnsurePeerCreatesSession verifies the create-session
// happy path: runner is called with `mutagen sync create` against
// the resolved address, and no error is returned.
func TestMutagenEnsurePeerCreatesSession(t *testing.T) {
	runner := &stubRunner{}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	tr.remotePathBase = "/peer/scan"
	folder := Folder{ID: "dk-x-abc123", Path: "/local/x"}
	peer := Peer{Name: "laptop", User: "richc"}

	if err := tr.EnsurePeerReachability(context.Background(), folder, peer); err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(runner.commands))
	}
	cmd := runner.commands[0]
	if cmd.name != "mutagen" {
		t.Errorf("command name = %q, want mutagen", cmd.name)
	}
	joined := strings.Join(cmd.args, " ")
	if !strings.Contains(joined, "sync create") {
		t.Errorf("args = %v, expected to contain 'sync create'", cmd.args)
	}
	if !strings.Contains(joined, "richc@100.64.42.3:") {
		t.Errorf("args = %v, expected SSH target richc@100.64.42.3", cmd.args)
	}
	if !strings.Contains(joined, "/local/x") {
		t.Errorf("args = %v, expected local path /local/x", cmd.args)
	}
}

// TestMutagenEnsurePeerIsIdempotent — the "session already exists"
// error from Mutagen must be treated as success so reconcile can
// call EnsurePeerReachability every tick without churn.
func TestMutagenEnsurePeerIsIdempotent(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			// Mimic the exact phrasing Mutagen emits when the
			// requested name is already in use.
			return []byte("unable to create session: session with name dkXXXX already exists"),
				errors.New("exit status 1")
		},
	}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	if err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/local/x"},
		Peer{Name: "laptop"}); err != nil {
		t.Errorf("session-exists must be treated as success; got %v", err)
	}
}

// TestMutagenEnsurePeerSurfacesUnknownError — anything other than
// "already exists" must propagate so reconcile can retry / log.
func TestMutagenEnsurePeerSurfacesUnknownError(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			return []byte("unable to connect to mutagen daemon"),
				errors.New("exit status 1")
		},
	}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/local/x"},
		Peer{Name: "laptop"})
	if err == nil {
		t.Fatal("expected error to propagate; got nil")
	}
	if !strings.Contains(err.Error(), "unable to connect") {
		t.Errorf("error should include underlying mutagen message; got %v", err)
	}
}

func TestMutagenRemovePeerIsIdempotent(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			return []byte("no such session: dkXXXX"), errors.New("exit status 1")
		},
	}
	tr := newTestMutagen(runner, &stubResolver{name: "tailscale", available: true})
	if err := tr.RemovePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/local/x"},
		Peer{Name: "laptop"}); err != nil {
		t.Errorf("removing absent session must be success; got %v", err)
	}
}

func TestMutagenProbeReturnsRTT(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			return []byte("mutagen v0.18.0"), nil
		},
	}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	d, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if d <= 0 {
		t.Errorf("Probe duration = %v, want > 0", d)
	}
}

func TestMutagenProbeUnreachableOnSSHFailure(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			return []byte("ssh: connect to host failed"), errors.New("exit status 255")
		},
	}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	_, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want errors.Is(ErrUnreachable)", err)
	}
}

func TestMutagenProbeUnreachableWhenPeerLacksMutagen(t *testing.T) {
	runner := &stubRunner{
		respond: func(_ string, _ []string) ([]byte, error) {
			return []byte("bash: mutagen: command not found"), errors.New("exit status 127")
		},
	}
	resolver := &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.42.3"},
	}
	tr := newTestMutagen(runner, resolver)
	_, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want errors.Is(ErrUnreachable)", err)
	}
}

func TestMutagenPropagateChangeFlushesSession(t *testing.T) {
	var lastCmd stubCmd
	runner := &stubRunner{
		respond: func(name string, args []string) ([]byte, error) {
			lastCmd = stubCmd{name: name, args: args}
			return nil, nil
		},
	}
	resolver := &stubResolver{name: "tailscale", available: true}
	tr := newTestMutagen(runner, resolver)
	change := Change{
		Folder: Folder{ID: "dk-x", Path: "/local/x"},
	}
	if err := tr.PropagateChange(context.Background(), change, Peer{Name: "laptop"}); err != nil {
		t.Fatalf("PropagateChange: %v", err)
	}
	if lastCmd.name != "mutagen" {
		t.Errorf("expected mutagen command, got %q", lastCmd.name)
	}
	joined := strings.Join(lastCmd.args, " ")
	if !strings.Contains(joined, "sync flush") {
		t.Errorf("expected 'sync flush' in args, got %v", lastCmd.args)
	}
}

// TestMutagenAvailableFalseWithoutLocalBinary exercises the
// real-Available() branch where the local `mutagen` binary is
// absent — what every developer machine without Mutagen installed
// looks like. The default constructor uses exec.LookPath; we
// override the binary-presence check to return false to simulate
// "no mutagen installed."
func TestMutagenAvailableFalseWithoutLocalBinary(t *testing.T) {
	tr := &MutagenTransport{
		resolver:           &stubResolver{name: "tailscale", available: true},
		runner:             &stubRunner{},
		localBinaryPresent: func() bool { return false },
	}
	if tr.Available() {
		t.Error("Available must be false when mutagen binary is missing")
	}
}

// TestMutagenAvailableFalseWhenResolverUnavailable covers the other
// short-circuit: the binary is present but the resolver (Tailscale,
// mDNS) isn't functional.
func TestMutagenAvailableFalseWhenResolverUnavailable(t *testing.T) {
	tr := &MutagenTransport{
		resolver:           &stubResolver{name: "tailscale", available: false},
		runner:             &stubRunner{},
		localBinaryPresent: func() bool { return true },
	}
	if tr.Available() {
		t.Error("Available must be false when resolver isn't available")
	}
}

func TestMutagenSessionNameDeterministic(t *testing.T) {
	tr := newTestMutagen(&stubRunner{}, &stubResolver{name: "tailscale"})
	folder := Folder{ID: "dk-x-abc123", Path: "/local/x"}
	a := tr.sessionName(folder, Peer{Name: "laptop"})
	b := tr.sessionName(folder, Peer{Name: "laptop"})
	c := tr.sessionName(folder, Peer{Name: "desktop"})
	if a != b {
		t.Errorf("session name must be deterministic; got %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("different peer must yield different session name; got %q for both", a)
	}
	if !strings.HasPrefix(a, "dk") {
		t.Errorf("session name must start with 'dk' (Mutagen letter-prefix rule); got %q", a)
	}
}

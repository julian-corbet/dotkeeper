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

// stubRunner records every command it's asked to run and returns
// canned responses. Lets gitssh tests exercise the transport logic
// without forking real git/ssh processes — those would require
// integration test infrastructure to set up authoritative peers.
type stubRunner struct {
	commands []stubCmd
	respond  func(name string, args []string) ([]byte, error)
}

type stubCmd struct {
	dir  string
	name string
	args []string
}

func (s *stubRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	s.commands = append(s.commands, stubCmd{dir: dir, name: name, args: append([]string(nil), args...)})
	if s.respond == nil {
		return nil, nil
	}
	return s.respond(name, args)
}

// stubResolver implements Resolver with a fixed name→address map.
type stubResolver struct {
	name       string
	available  bool
	addresses  map[string]string // peer.Name -> address
	resolveErr error             // when set, returned for every Resolve
}

func (s *stubResolver) Name() string  { return s.name }
func (s *stubResolver) Available() bool { return s.available }
func (s *stubResolver) Resolve(_ context.Context, peer Peer) (string, error) {
	if s.resolveErr != nil {
		return "", s.resolveErr
	}
	if addr, ok := s.addresses[peer.Name]; ok {
		return addr, nil
	}
	return "", ErrPeerUnknown
}

func newTestGitSSH(runner commandRunner, resolver Resolver) *GitSSHTransport {
	return &GitSSHTransport{
		resolver:     resolver,
		runner:       runner,
		probeTimeout: time.Second,
		pushTimeout:  time.Second,
	}
}

func TestGitSSHNameIncludesResolverSuffix(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale"})
	if tr.Name() != "git-ssh+tailscale" {
		t.Errorf("Name = %q, want git-ssh+tailscale", tr.Name())
	}
}

func TestGitSSHUnavailableWhenResolverUnavailable(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale", available: false})
	if tr.Available() {
		t.Error("transport reports available when resolver isn't")
	}
}

func TestEnsurePeerSetsRemoteOnExisting(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/tmp/repo"}, Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}

	// First call must be set-url; if it succeeds (stub returns nil),
	// no add is attempted.
	if len(runner.commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(runner.commands))
	}
	cmd := runner.commands[0]
	if cmd.name != "git" || cmd.args[0] != "remote" || cmd.args[1] != "set-url" {
		t.Errorf("first command not git remote set-url: %+v", cmd)
	}
	if cmd.dir != "/tmp/repo" {
		t.Errorf("git invoked in wrong dir: %s", cmd.dir)
	}
}

func TestEnsurePeerFallsBackToAddOnSetURLNotFound(t *testing.T) {
	calls := 0
	runner := &stubRunner{respond: func(_ string, args []string) ([]byte, error) {
		calls++
		if calls == 1 && len(args) >= 2 && args[1] == "set-url" {
			return []byte("error: No such remote 'dk+tailscale+laptop'\n"),
				errors.New("exit status 128")
		}
		return nil, nil
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/tmp/repo"}, Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 git calls (set-url, then add), got %d", calls)
	}
}

func TestEnsurePeerSurfacesNonRecoverableError(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) {
		// set-url fails with a permission error rather than a
		// "no such remote." Must NOT fall back to add — the
		// add would also fail, just with a different message.
		return []byte("fatal: not a git repository\n"), errors.New("exit status 128")
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/tmp/repo"}, Peer{Name: "laptop"})
	if err == nil {
		t.Error("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error should surface git's message; got %v", err)
	}
	if len(runner.commands) != 1 {
		t.Errorf("must not have fallen back to add; %d commands ran", len(runner.commands))
	}
}

func TestEnsurePeerWrapsUnknownResolverError(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{}, // no entry for "laptop"
	})

	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: "/tmp/repo"}, Peer{Name: "laptop"})
	if !errors.Is(err, ErrPeerUnknown) {
		t.Errorf("expected error to wrap ErrPeerUnknown; got %v", err)
	}
}

func TestProbeRoundTrips(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) {
		// Simulate a 10ms SSH RTT.
		time.Sleep(10 * time.Millisecond)
		return nil, nil
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	d, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if d < 5*time.Millisecond {
		t.Errorf("Probe latency %v unreasonably small for a 10ms simulated RTT", d)
	}
}

func TestProbeReturnsUnreachableWhenResolverFails(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{},
	})

	_, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable when resolver can't find peer; got %v", err)
	}
}

func TestProbeReturnsUnreachableWhenSSHFails(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) {
		return []byte("ssh: connect to host: connection refused\n"), errors.New("exit status 255")
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	_, err := tr.Probe(context.Background(), Peer{Name: "laptop"})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable when SSH fails; got %v", err)
	}
}

func TestPropagateChangePushes(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "dk-x", Path: "/tmp/repo"}, CommitHash: "abc123"},
		Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("PropagateChange: %v", err)
	}

	if len(runner.commands) != 1 {
		t.Fatalf("expected 1 git command, got %d", len(runner.commands))
	}
	cmd := runner.commands[0]
	if cmd.name != "git" || cmd.args[0] != "push" {
		t.Errorf("expected git push, got %+v", cmd)
	}
	// Push refspec must be "abc123:refs/heads/main" — push the
	// supplied commit hash to the peer's main branch. updateInstead
	// on the peer side then updates the working tree atomically.
	joined := strings.Join(cmd.args, " ")
	if !strings.Contains(joined, "abc123:refs/heads/main") {
		t.Errorf("expected refspec abc123:refs/heads/main, got args: %v", cmd.args)
	}
}

func TestPropagateChangeUsesHeadWhenCommitHashEmpty(t *testing.T) {
	// dotkeeper's reconcile may invoke PropagateChange without
	// knowing the specific commit hash (e.g. "push whatever we
	// just committed"). Push HEAD in that case.
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "dk-x", Path: "/tmp/repo"}},
		Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("PropagateChange: %v", err)
	}
	cmd := runner.commands[0]
	joined := strings.Join(cmd.args, " ")
	if !strings.Contains(joined, "HEAD:refs/heads/main") {
		t.Errorf("expected refspec HEAD:refs/heads/main when no commit hash; got %v", cmd.args)
	}
}

func TestPropagateChangeSurfacesPushError(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) {
		return []byte("remote: fatal: repository '/home/richc/.local/share/dotkeeper/repos/dk-x.git' not found\n"),
			errors.New("exit status 128")
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "dk-x", Path: "/tmp/repo"}, CommitHash: "abc"},
		Peer{Name: "laptop"})
	if err == nil {
		t.Error("expected error from failed push")
	}
	if !strings.Contains(err.Error(), "repository") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should surface git's message verbatim so operator can act; got %v", err)
	}
}

func TestRemotePeerName(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale"})
	// Name sanitisation: spaces become hyphens, alphanumerics
	// preserve case.
	got := tr.remoteName(Peer{Name: "WORK-Laptop"})
	want := "dk+tailscale+WORK-Laptop"
	if got != want {
		t.Errorf("remoteName = %q, want %q", got, want)
	}
	got = tr.remoteName(Peer{Name: "machine with spaces"})
	want = "dk+tailscale+machine-with-spaces"
	if got != want {
		t.Errorf("remoteName for spaces = %q, want %q", got, want)
	}
}

func TestRemoteURL(t *testing.T) {
	// v1.0.0: remoteURL points at the peer's working tree directly
	// (mirrored path). dotkeeper bare-init configures the peer
	// with receive.denyCurrentBranch=updateInstead so the push
	// updates the working tree.
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale"})
	got := tr.remoteURL("100.64.0.5", Peer{Name: "laptop"},
		Folder{ID: "dk-x", Path: "/home/richc/Documents/GitHub/dotkeeper"})
	want := "ssh://100.64.0.5/home/richc/Documents/GitHub/dotkeeper"
	if got != want {
		t.Errorf("remoteURL = %q, want %q", got, want)
	}
	// With explicit user.
	got = tr.remoteURL("100.64.0.5", Peer{Name: "laptop", User: "richc"},
		Folder{ID: "dk-x", Path: "/home/richc/Documents/GitHub/dotkeeper"})
	want = "ssh://richc@100.64.0.5/home/richc/Documents/GitHub/dotkeeper"
	if got != want {
		t.Errorf("remoteURL with user = %q, want %q", got, want)
	}
}

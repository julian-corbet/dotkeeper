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

// GitSSHTransport runs `git push` inline and only returns after the
// remote ACKs. The elapsed duration of PropagateChange is therefore
// a real measurement of work done and a valid input to the cost
// model. PropagatesSynchronously must report true.
func TestGitSSHPropagatesSynchronouslyIsTrue(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale", available: true})
	if !tr.PropagatesSynchronously() {
		t.Error("GitSSHTransport.PropagatesSynchronously must be true; PropagateChange blocks on git push")
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

// branchProbeResponder builds a stubRunner respond function that
// answers `symbolic-ref --short HEAD` with branchName and forwards
// any other invocation to the supplied inner responder (nil-safe:
// returns nil/nil when no inner). Used by tests that exercise
// PropagateChange's two-step (resolve-branch, then push) flow.
func branchProbeResponder(branchName string, inner func(name string, args []string) ([]byte, error)) func(string, []string) ([]byte, error) {
	return func(name string, args []string) ([]byte, error) {
		if name == "git" && len(args) >= 1 && args[0] == "symbolic-ref" {
			return []byte(branchName + "\n"), nil
		}
		if inner == nil {
			return nil, nil
		}
		return inner(name, args)
	}
}

// findCommand returns the first stubCmd whose first arg equals
// firstArg, or nil if none was recorded. Used by tests to assert
// on the `push` invocation independently of any `symbolic-ref`
// probe that ran first.
func findCommand(cmds []stubCmd, firstArg string) *stubCmd {
	for i := range cmds {
		if cmds[i].name == "git" && len(cmds[i].args) >= 1 && cmds[i].args[0] == firstArg {
			return &cmds[i]
		}
	}
	return nil
}

func TestPropagateChangePushes(t *testing.T) {
	runner := &stubRunner{respond: branchProbeResponder("main", nil)}
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

	push := findCommand(runner.commands, "push")
	if push == nil {
		t.Fatalf("no git push recorded; commands=%+v", runner.commands)
	}
	// Push refspec must be "abc123:refs/heads/main" — push the
	// supplied commit hash to the peer's main branch. updateInstead
	// on the peer side then updates the working tree atomically.
	joined := strings.Join(push.args, " ")
	if !strings.Contains(joined, "abc123:refs/heads/main") {
		t.Errorf("expected refspec abc123:refs/heads/main, got args: %v", push.args)
	}
}

func TestPropagateChangeUsesHeadWhenCommitHashEmpty(t *testing.T) {
	// dotkeeper's reconcile may invoke PropagateChange without
	// knowing the specific commit hash (e.g. "push whatever we
	// just committed"). Push HEAD in that case.
	runner := &stubRunner{respond: branchProbeResponder("main", nil)}
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
	push := findCommand(runner.commands, "push")
	if push == nil {
		t.Fatalf("no git push recorded; commands=%+v", runner.commands)
	}
	joined := strings.Join(push.args, " ")
	if !strings.Contains(joined, "HEAD:refs/heads/main") {
		t.Errorf("expected refspec HEAD:refs/heads/main when no commit hash; got %v", push.args)
	}
}

// TestPropagateChangePushesToLocalBranchName proves the v1.0.1
// fix: when the local checkout is on a branch other than `main`,
// PropagateChange must push to the matching branch on the peer.
// Pre-fix the destination was hardcoded to `refs/heads/main`, which
// silently bricked propagation for any repo on master/dev/etc. —
// updateInstead on the peer never fired because the push landed
// on a stale main rather than the peer's checked-out branch.
func TestPropagateChangePushesToLocalBranchName(t *testing.T) {
	runner := &stubRunner{respond: branchProbeResponder("dev", nil)}
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

	push := findCommand(runner.commands, "push")
	if push == nil {
		t.Fatalf("no git push recorded; commands=%+v", runner.commands)
	}
	joined := strings.Join(push.args, " ")
	if !strings.Contains(joined, "abc123:refs/heads/dev") {
		t.Errorf("expected refspec abc123:refs/heads/dev for repo on `dev` branch; got args: %v", push.args)
	}
}

// TestPropagateChangeFallsBackToMainOnDetachedHEAD pins the
// conservative fallback: when `git symbolic-ref` fails (detached
// HEAD, not a repo, runner error), PropagateChange must still
// attempt a push — to `refs/heads/main` — rather than abort. An
// explicit error path would prevent every detached-HEAD scenario
// from ever propagating; the better failure mode is to push and
// let the peer reject if the branch is wrong, which the operator
// will see in the push error.
func TestPropagateChangeFallsBackToMainOnDetachedHEAD(t *testing.T) {
	runner := &stubRunner{respond: func(name string, args []string) ([]byte, error) {
		if name == "git" && len(args) >= 1 && args[0] == "symbolic-ref" {
			return []byte("fatal: ref HEAD is not a symbolic ref\n"),
				errors.New("exit status 128")
		}
		return nil, nil
	}}
	tr := newTestGitSSH(runner, &stubResolver{
		name:      "tailscale",
		available: true,
		addresses: map[string]string{"laptop": "100.64.0.5"},
	})

	err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "dk-x", Path: "/tmp/repo"}, CommitHash: "abc123"},
		Peer{Name: "laptop"})
	if err != nil {
		t.Fatalf("PropagateChange should fall back to main, not surface symbolic-ref failure: %v", err)
	}

	push := findCommand(runner.commands, "push")
	if push == nil {
		t.Fatalf("no git push recorded after symbolic-ref failure; commands=%+v", runner.commands)
	}
	joined := strings.Join(push.args, " ")
	if !strings.Contains(joined, "abc123:refs/heads/main") {
		t.Errorf("expected fallback refspec abc123:refs/heads/main; got args: %v", push.args)
	}
}

func TestPropagateChangeSurfacesPushError(t *testing.T) {
	runner := &stubRunner{respond: func(_ string, _ []string) ([]byte, error) {
		return []byte("remote: fatal: repository '/srv/example/repos/dk-x.git' not found\n"),
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

func TestRemoteURLBracketsIPv6(t *testing.T) {
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale"})
	cases := []struct {
		addr string
		user string
		want string
	}{
		// IPv4 stays as-is.
		{"100.64.0.5", "", "ssh://100.64.0.5/repo"},
		{"100.64.0.5", "alice", "ssh://alice@100.64.0.5/repo"},
		// IPv6 must be bracketed per RFC 3986. Without brackets,
		// the URL parser would treat the embedded colons as
		// host:port separators and the path would lose its
		// leading slash. Real-world impact: Tailscale operators
		// running IPv6-only mesh setups would see "ssh: Could
		// not resolve hostname fe80" failures.
		{"fe80::1", "", "ssh://[fe80::1]/repo"},
		{"fe80::1", "alice", "ssh://alice@[fe80::1]/repo"},
		{"fd7a:115c:a1e0::1", "", "ssh://[fd7a:115c:a1e0::1]/repo"},
		// Already-bracketed addresses must not be re-bracketed.
		{"[fe80::1]", "", "ssh://[fe80::1]/repo"},
		// host:port shape (one colon) must NOT be bracketed — that
		// would produce ssh://[host:port]/path which SSH treats as
		// an unresolvable IPv6 literal. A future static-config
		// resolver might emit host:port; this case guards against
		// the previous over-eager bracketing rule.
		{"example.com:2222", "", "ssh://example.com:2222/repo"},
		{"example.com:2222", "alice", "ssh://alice@example.com:2222/repo"},
	}
	for _, c := range cases {
		got := tr.remoteURL(c.addr, Peer{Name: "p", User: c.user}, Folder{ID: "x", Path: "/repo"})
		if got != c.want {
			t.Errorf("remoteURL(%q, user=%q) = %q, want %q", c.addr, c.user, got, c.want)
		}
	}
}

func TestRemoteURL(t *testing.T) {
	// v1.0.0: remoteURL points at the peer's working tree directly
	// (mirrored path). dotkeeper bare-init configures the peer
	// with receive.denyCurrentBranch=updateInstead so the push
	// updates the working tree.
	tr := newTestGitSSH(&stubRunner{}, &stubResolver{name: "tailscale"})
	got := tr.remoteURL("100.64.0.5", Peer{Name: "laptop"},
		Folder{ID: "dk-x", Path: "/srv/example/repo"})
	want := "ssh://100.64.0.5/srv/example/repo"
	if got != want {
		t.Errorf("remoteURL = %q, want %q", got, want)
	}
	// With explicit user.
	got = tr.remoteURL("100.64.0.5", Peer{Name: "laptop", User: "alice"},
		Folder{ID: "dk-x", Path: "/srv/example/repo"})
	want = "ssh://alice@100.64.0.5/srv/example/repo"
	if got != want {
		t.Errorf("remoteURL with user = %q, want %q", got, want)
	}
}

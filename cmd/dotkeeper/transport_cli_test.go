// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/transport"
)

func mkdirAll(dir string, perm uint32) error    { return os.MkdirAll(dir, os.FileMode(perm)) }
func writeFile(path string, data []byte, perm uint32) error {
	return os.WriteFile(path, data, os.FileMode(perm))
}

// fakeTransport implements transport.Transport with knobs for
// Available/Name/probe outcome. Used by the CLI tests to exercise
// formatting and routing without standing up real Syncthing or
// real SSH.
type fakeTransport struct {
	name        string
	available   bool
	probeLatency time.Duration
	probeErr    error
}

func (f *fakeTransport) Name() string  { return f.name }
func (f *fakeTransport) Available() bool { return f.available }
func (f *fakeTransport) EnsurePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (f *fakeTransport) RemovePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (f *fakeTransport) Probe(_ context.Context, _ transport.Peer) (time.Duration, error) {
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	return f.probeLatency, nil
}
func (f *fakeTransport) PropagateChange(_ context.Context, _ transport.Change, _ transport.Peer) error {
	return nil
}
func (f *fakeTransport) PropagatesSynchronously() bool { return true }

// fakeResolver implements transport.Resolver — used by the
// resolveBareInitAddress tests below.
type fakeResolver struct {
	name      string
	available bool
	answers   map[string]string // peer.Name -> address
	err       error             // when set, returned for every Resolve
}

func (f *fakeResolver) Name() string  { return f.name }
func (f *fakeResolver) Available() bool { return f.available }
func (f *fakeResolver) Resolve(_ context.Context, peer transport.Peer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if addr, ok := f.answers[peer.Name]; ok {
		return addr, nil
	}
	return "", transport.ErrPeerUnknown
}

// --- transport list / status CLI tests ---

func TestTransportListCommandRendersTable(t *testing.T) {
	original := transportSource
	defer func() { transportSource = original }()
	transportSource = func() []transport.Transport {
		return []transport.Transport{
			&fakeTransport{name: "git-ssh+tailscale", available: true},
			&fakeTransport{name: "git-ssh+mdns", available: false},
			&fakeTransport{name: "syncthing", available: true},
		}
	}

	cmd := transportListCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("transport list: %v", err)
	}
	rendered := out.String()

	// Header is mandatory; without it operators can't read the
	// table.
	if !strings.Contains(rendered, "TRANSPORT") || !strings.Contains(rendered, "AVAILABLE") {
		t.Errorf("output missing header row:\n%s", rendered)
	}
	for _, want := range []string{
		"git-ssh+tailscale",
		"git-ssh+mdns",
		"syncthing",
		"unavailable", // git-ssh+mdns is not available
		"available",   // the other two are
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q:\n%s", want, rendered)
		}
	}
}

func TestTransportListCommandEmptyTransports(t *testing.T) {
	// Defensive: when no transports are configured, the command
	// should still produce a usable (header-only) table rather
	// than crashing or returning an error.
	original := transportSource
	defer func() { transportSource = original }()
	transportSource = func() []transport.Transport { return nil }

	cmd := transportListCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("transport list with empty sources: %v", err)
	}
	if !strings.Contains(out.String(), "TRANSPORT") {
		t.Errorf("empty-sources output missing header:\n%s", out.String())
	}
}

func TestTransportStatusCommandRendersPerPeerTable(t *testing.T) {
	// Set up a temp XDG_CONFIG_HOME with a machine.toml that has
	// one peer. transport status will load it, run discovery
	// against the (stubbed) transports, and print the per-peer
	// route table.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := config.WriteMachineConfigV2(&config.MachineConfigV2{
		SchemaVersion: 2,
		Name:          "this-machine",
		Slot:          0,
		Peers: []config.PeerEntry{
			{Name: "remote-peer", DeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"},
		},
		Discovery: config.DiscoveryConfig{
			ScanRoots: []string{tmp + "/scan"},
		},
	}); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}

	originalSource := transportSource
	defer func() { transportSource = originalSource }()
	transportSource = func() []transport.Transport {
		return []transport.Transport{
			&fakeTransport{name: "git-ssh+tailscale", available: true, probeLatency: 7 * time.Millisecond},
			&fakeTransport{name: "syncthing", available: true, probeLatency: 1 * time.Millisecond},
		}
	}

	cmd := transportStatusCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("transport status: %v", err)
	}
	rendered := out.String()

	// Header columns: PEER, TRANSPORT, REACHABLE, PROBE-RTT,
	// SETUP-MS, MB-PER-SEC, N. Skipping any of these is
	// information loss the operator can't recover from without
	// re-running.
	for _, want := range []string{"PEER", "TRANSPORT", "REACHABLE", "PROBE-RTT", "SETUP-MS", "MB-PER-SEC", "N"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing column %q:\n%s", want, rendered)
		}
	}
	// Per-peer row content: the peer name, both transport names,
	// and "yes" reachability indicators because both fakes report
	// success.
	for _, want := range []string{"remote-peer", "git-ssh+tailscale", "syncthing", "yes"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing per-peer content %q:\n%s", want, rendered)
		}
	}
}

func TestTransportStatusCommandFiltersToNamedPeer(t *testing.T) {
	// status accepts an optional [peer] positional. Should filter
	// to only that peer's row and return an error when the peer
	// isn't in machine.toml — better than silently producing an
	// empty table that looks like "discovery returned nothing."
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if err := config.WriteMachineConfigV2(&config.MachineConfigV2{
		SchemaVersion: 2,
		Name:          "this-machine",
		Peers: []config.PeerEntry{
			{Name: "alpha", DeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"},
			{Name: "beta", DeviceID: "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"},
		},
		Discovery: config.DiscoveryConfig{ScanRoots: []string{tmp + "/scan"}},
	}); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}
	originalSource := transportSource
	defer func() { transportSource = originalSource }()
	transportSource = func() []transport.Transport {
		return []transport.Transport{&fakeTransport{name: "syncthing", available: true, probeLatency: time.Millisecond}}
	}

	// Filter to "alpha" — only that peer should appear.
	cmd := transportStatusCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"alpha"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status alpha: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "alpha") {
		t.Errorf("filtered-to-alpha output missing alpha:\n%s", rendered)
	}
	if strings.Contains(rendered, "beta") {
		t.Errorf("filtered-to-alpha output included beta:\n%s", rendered)
	}

	// Unknown peer name — error.
	cmd2 := transportStatusCmd()
	out2 := &bytes.Buffer{}
	cmd2.SetOut(out2)
	cmd2.SetErr(out2)
	cmd2.SilenceUsage = true
	cmd2.SetArgs([]string{"never-paired"})
	if err := cmd2.Execute(); err == nil {
		t.Error("status with unknown peer should return an error; got nil")
	}
}

// --- resolveBareInitAddress tests ---

func TestResolveBareInitAddressOverrideWins(t *testing.T) {
	// Override should short-circuit every resolver: the override
	// is the operator saying "I know this address; don't ask
	// anyone else."
	called := false
	resolvers := []transport.Resolver{
		&fakeResolver{
			name:      "always-asked",
			available: true,
			answers: map[string]string{
				"laptop": "should-not-be-used",
			},
		},
	}
	// Wrap to detect resolver invocation.
	wrapper := &spyingResolver{inner: resolvers[0], hit: &called}
	resolvers[0] = wrapper

	addr, source, err := resolveBareInitAddress(context.Background(),
		config.PeerEntry{Name: "laptop"},
		"alice@override.example",
		resolvers)
	if err != nil {
		t.Fatalf("resolveBareInitAddress: %v", err)
	}
	if addr != "alice@override.example" || source != "override" {
		t.Errorf("override path returned addr=%q source=%q; want alice@override.example, override", addr, source)
	}
	if called {
		t.Error("resolver was called despite override being set; override must short-circuit")
	}
}

func TestResolveBareInitAddressFallsThroughResolvers(t *testing.T) {
	// First resolver doesn't know the peer; second does. The
	// returned source should name the resolver that succeeded.
	resolvers := []transport.Resolver{
		&fakeResolver{name: "first", available: true, answers: map[string]string{}},
		&fakeResolver{name: "second", available: true, answers: map[string]string{"laptop": "100.64.0.5"}},
	}
	addr, source, err := resolveBareInitAddress(context.Background(),
		config.PeerEntry{Name: "laptop"}, "", resolvers)
	if err != nil {
		t.Fatalf("resolveBareInitAddress: %v", err)
	}
	if addr != "100.64.0.5" {
		t.Errorf("expected addr from second resolver; got %q", addr)
	}
	if source != "second" {
		t.Errorf("expected source 'second'; got %q", source)
	}
}

func TestResolveBareInitAddressSkipsUnavailableResolvers(t *testing.T) {
	// Available()=false means the resolver shouldn't be asked.
	called := false
	resolvers := []transport.Resolver{
		&spyingResolver{
			inner: &fakeResolver{name: "broken", available: false},
			hit:   &called,
		},
		&fakeResolver{name: "ok", available: true, answers: map[string]string{"laptop": "10.0.0.1"}},
	}
	addr, source, _ := resolveBareInitAddress(context.Background(),
		config.PeerEntry{Name: "laptop"}, "", resolvers)
	if called {
		t.Error("unavailable resolver was asked; Available() gate is mandatory")
	}
	if addr != "10.0.0.1" || source != "ok" {
		t.Errorf("expected fallthrough to 'ok' resolver; got addr=%q source=%q", addr, source)
	}
}

func TestResolveBareInitAddressNameAsHostnameFallback(t *testing.T) {
	// All resolvers either unavailable or unknown-peer. The
	// last-resort fallback is "use peer.Name as the SSH target,"
	// which works for /etc/hosts / ~/.ssh/config / LAN DNS setups.
	resolvers := []transport.Resolver{
		&fakeResolver{name: "first", available: true, answers: map[string]string{}},
	}
	addr, source, err := resolveBareInitAddress(context.Background(),
		config.PeerEntry{Name: "laptop-on-ssh-config"}, "", resolvers)
	if err != nil {
		t.Fatalf("resolveBareInitAddress: %v", err)
	}
	if addr != "laptop-on-ssh-config" {
		t.Errorf("expected name-as-hostname fallback; got addr=%q", addr)
	}
	if source != "name-as-hostname" {
		t.Errorf("expected source 'name-as-hostname'; got %q", source)
	}
}

func TestResolveBareInitAddressContinuesPastErroringResolvers(t *testing.T) {
	// A resolver returning ErrResolverUnavailable (network down,
	// daemon not running) must not abort the whole resolution
	// ladder. We should move on to subsequent resolvers and the
	// fallback.
	resolvers := []transport.Resolver{
		&fakeResolver{name: "broken-but-listed", available: true, err: transport.ErrResolverUnavailable},
		&fakeResolver{name: "fine", available: true, answers: map[string]string{"laptop": "10.0.0.7"}},
	}
	addr, source, _ := resolveBareInitAddress(context.Background(),
		config.PeerEntry{Name: "laptop"}, "", resolvers)
	if addr != "10.0.0.7" || source != "fine" {
		t.Errorf("expected fallthrough past erroring resolver; got addr=%q source=%q", addr, source)
	}
}

// spyingResolver wraps another resolver and notes whether it was
// invoked. Useful for testing short-circuit behaviour (override,
// unavailable) without polluting the fakeResolver fields.
type spyingResolver struct {
	inner transport.Resolver
	hit   *bool
}

func (s *spyingResolver) Name() string  { return s.inner.Name() }
func (s *spyingResolver) Available() bool { return s.inner.Available() }
func (s *spyingResolver) Resolve(ctx context.Context, peer transport.Peer) (string, error) {
	*s.hit = true
	return s.inner.Resolve(ctx, peer)
}

// --- bareInitCmd end-to-end tests (with stubbed ssh + temp XDG) ---

func TestBareInitCmdRunsConfigForEachFolderAndPeer(t *testing.T) {
	// Full bare-init flow: load machine.toml, iterate peers ×
	// folders, invoke the SSH runner. With one peer and one
	// folder, we expect exactly one SSH call carrying the
	// `git -C <folder> config receive.denyCurrentBranch updateInstead`
	// command.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	folder := tmp + "/Code/proj"
	if err := writeManagedFolder(folder); err != nil {
		t.Fatalf("write folder marker: %v", err)
	}
	if err := config.WriteMachineConfigV2(&config.MachineConfigV2{
		SchemaVersion: 2,
		Name:          "this-machine",
		Peers: []config.PeerEntry{
			{Name: "remote-laptop", DeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"},
		},
		Discovery: config.DiscoveryConfig{ScanRoots: []string{tmp + "/Code"}},
	}); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}

	original := bareInitSSHRunner
	defer func() { bareInitSSHRunner = original }()
	var calls []sshCall
	bareInitSSHRunner = func(_ context.Context, target string, args ...string) ([]byte, error) {
		calls = append(calls, sshCall{target: target, args: append([]string(nil), args...)})
		return nil, nil // pretend ssh succeeded
	}

	// Use --host to bypass the Tailscale resolver path (the test
	// host may or may not have tailscale installed; we don't care
	// about the address-resolution ladder here, just the SSH
	// dispatch).
	cmd := bareInitCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--peer=remote-laptop", "--host=alice@10.0.0.5"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("bare-init: %v\noutput:\n%s", err, out.String())
	}

	// managedFolderPathsV5 always includes the dotkeeper config
	// dir alongside any user-tracked folder, so with one peer
	// and one tracked folder we get 2 invocations (config dir +
	// the test's folder). Both must target the same SSH endpoint
	// with the same `git config` command shape.
	if len(calls) < 1 {
		t.Fatalf("expected at least 1 SSH invocation, got 0:\n%s", out.String())
	}
	for _, c := range calls {
		if c.target != "alice@10.0.0.5" {
			t.Errorf("ssh target = %q, want alice@10.0.0.5", c.target)
		}
		joined := strings.Join(c.args, " ")
		for _, want := range []string{"git", "config", "receive.denyCurrentBranch", "updateInstead"} {
			if !strings.Contains(joined, want) {
				t.Errorf("ssh command missing %q; full args: %v", want, c.args)
			}
		}
	}
	// The test's tracked folder must appear in at least one call.
	found := false
	for _, c := range calls {
		if strings.Contains(strings.Join(c.args, " "), folder) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("none of the SSH calls referenced the tracked folder %q; calls=%v", folder, calls)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("operator output should report 'ok' for the successful folder; got:\n%s", out.String())
	}
}

func TestBareInitCmdReportsErrorsForFailedPeers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	folder := tmp + "/Code/proj"
	if err := writeManagedFolder(folder); err != nil {
		t.Fatalf("write folder marker: %v", err)
	}
	if err := config.WriteMachineConfigV2(&config.MachineConfigV2{
		SchemaVersion: 2,
		Name:          "this-machine",
		Peers: []config.PeerEntry{
			{Name: "broken-laptop", DeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"},
		},
		Discovery: config.DiscoveryConfig{ScanRoots: []string{tmp + "/Code"}},
	}); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}

	original := bareInitSSHRunner
	defer func() { bareInitSSHRunner = original }()
	bareInitSSHRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("ssh: connect to host: connection refused"), errors.New("exit status 255")
	}

	cmd := bareInitCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--peer=broken-laptop", "--host=alice@10.0.0.5"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected bare-init to return non-nil error when SSH fails; got nil")
	}
	if !strings.Contains(out.String(), "FAILED") {
		t.Errorf("operator output should mark the failed folder as FAILED; got:\n%s", out.String())
	}
}

type sshCall struct {
	target string
	args   []string
}

// writeManagedFolder creates the minimum on-disk artefact that
// makes managedFolderPathsV5 see folder as a managed folder: a
// .dotkeeper.toml file at the path.
func writeManagedFolder(folder string) error {
	if err := osMkdirAll(folder, 0o755); err != nil {
		return err
	}
	return osWriteFile(folder+"/.dotkeeper.toml", []byte("schema_version = 2\n[repo]\nname = \"proj\"\n"), 0o644)
}

// Small wrappers to keep the test file's import list tight.
func osMkdirAll(dir string, perm uint32) error { return mkdirAll(dir, perm) }
func osWriteFile(path string, data []byte, perm uint32) error {
	return writeFile(path, data, perm)
}

// --- bareInitSSHRunner injection tests ---

func TestBareInitSSHRunnerInjection(t *testing.T) {
	// Smoke test for the injection seam used by bareInitCmd tests:
	// swap in a stub, observe args, restore. Direct behavioural
	// tests of bareInitCmd itself need machine.toml + folder
	// fixtures; this test pins the seam so a future refactor
	// that drops it gets caught.
	original := bareInitSSHRunner
	defer func() { bareInitSSHRunner = original }()

	var capturedTarget string
	var capturedArgs []string
	bareInitSSHRunner = func(_ context.Context, target string, args ...string) ([]byte, error) {
		capturedTarget = target
		capturedArgs = append([]string(nil), args...)
		return nil, nil
	}

	out, err := bareInitSSHRunner(context.Background(), "alice@laptop",
		"git", "-C", "/tmp/repo", "config", "receive.denyCurrentBranch", "updateInstead")
	if err != nil {
		t.Fatalf("stub runner: %v", err)
	}
	if string(out) != "" {
		t.Errorf("expected empty output from stub; got %q", out)
	}
	if capturedTarget != "alice@laptop" {
		t.Errorf("target = %q, want alice@laptop", capturedTarget)
	}
	wantArgs := []string{"git", "-C", "/tmp/repo", "config", "receive.denyCurrentBranch", "updateInstead"}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("args length = %d, want %d", len(capturedArgs), len(wantArgs))
	}
	for i := range wantArgs {
		if capturedArgs[i] != wantArgs[i] {
			t.Errorf("args[%d] = %q, want %q", i, capturedArgs[i], wantArgs[i])
		}
	}
}

func TestBareInitSSHRunnerErrorBubblesUp(t *testing.T) {
	original := bareInitSSHRunner
	defer func() { bareInitSSHRunner = original }()
	wantErr := errors.New("ssh: connect to host: connection refused")
	bareInitSSHRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("ssh: Could not resolve hostname"), wantErr
	}
	out, err := bareInitSSHRunner(context.Background(), "x")
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(string(out), "ssh: Could not resolve") {
		t.Errorf("captured output = %q; expected ssh stderr to flow through", out)
	}
}

// --- shellQuote tests ---

func TestShellQuoteHandlesPathsWithSpaces(t *testing.T) {
	// Verifies the bare-init path-quoting helper: paths with
	// spaces, single quotes, and the empty string all need to
	// round-trip cleanly through `sh -c '<arg>'` on the remote
	// side.
	cases := []struct {
		in, want string
	}{
		{"/simple", "'/simple'"},
		{"/with space", "'/with space'"},
		{"/Users/me/My Documents", "'/Users/me/My Documents'"},
		{"/has'apostrophe", `'/has'\''apostrophe'`},
		{"", "''"},
	}
	for _, c := range cases {
		got := shellQuote(c.in)
		if got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

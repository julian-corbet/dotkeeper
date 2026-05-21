// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// makeStubTailscale writes a small shell script (POSIX) or batch
// file (Windows) that emits the given JSON when invoked as
// "tailscale status --json". Returns the path to set as the
// resolver's binary. Cleanup is registered with t.Cleanup so the
// stub disappears after the test.
//
// Cross-platform care: on Windows the harness would need a .bat,
// which makes the cross-platform testing story more complex. The
// resolver tests skip on Windows; the resolver itself remains
// portable because the only platform-specific code is the
// exec.LookPath lookup, which is identical across OSes.
func makeStubTailscale(t *testing.T, json string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub tailscale via shell script not supported on Windows; resolver logic is platform-independent")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "tailscale")
	body := "#!/bin/sh\ncat <<'EOF'\n" + json + "\nEOF\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return script
}

const goodTailscaleStatus = `{
  "Self": {"HostName": "self-machine", "TailscaleIPs": ["100.64.0.1"]},
  "Peer": {
    "nodekey:abc": {"HostName": "peer-one", "TailscaleIPs": ["100.64.0.2"], "Online": true},
    "nodekey:def": {"HostName": "peer-two", "TailscaleIPs": ["100.64.0.3"], "Online": false}
  }
}`

func TestTailscaleResolverParsesStatus(t *testing.T) {
	bin := makeStubTailscale(t, goodTailscaleStatus)
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	addr, err := r.Resolve(context.Background(), Peer{Name: "peer-one"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if addr != "100.64.0.2" {
		t.Errorf("Resolve = %q, want 100.64.0.2", addr)
	}
}

func TestTailscaleResolverHostnameMatchingIsCaseInsensitive(t *testing.T) {
	bin := makeStubTailscale(t, goodTailscaleStatus)
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	// Tailscale lowercases hostnames; dotkeeper Peer.Name may be
	// mixed-case. Should match regardless.
	addr, err := r.Resolve(context.Background(), Peer{Name: "PEER-ONE"})
	if err != nil {
		t.Fatalf("Resolve with uppercase Name: %v", err)
	}
	if addr != "100.64.0.2" {
		t.Errorf("Resolve = %q, want 100.64.0.2", addr)
	}
}

func TestTailscaleResolverOfflinePeerStillResolved(t *testing.T) {
	bin := makeStubTailscale(t, goodTailscaleStatus)
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	// peer-two is offline but still has an IP. The resolver returns
	// the IP; reachability is the Probe's job, not the resolver's.
	addr, err := r.Resolve(context.Background(), Peer{Name: "peer-two"})
	if err != nil {
		t.Fatalf("offline peer should still resolve to IP; got %v", err)
	}
	if addr != "100.64.0.3" {
		t.Errorf("Resolve = %q, want 100.64.0.3", addr)
	}
}

func TestTailscaleResolverUnknownPeer(t *testing.T) {
	bin := makeStubTailscale(t, goodTailscaleStatus)
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	_, err := r.Resolve(context.Background(), Peer{Name: "not-in-tailnet"})
	if !errors.Is(err, ErrPeerUnknown) {
		t.Errorf("expected ErrPeerUnknown for unknown peer; got %v", err)
	}
}

func TestTailscaleResolverMalformedJSON(t *testing.T) {
	bin := makeStubTailscale(t, "{not valid json")
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	_, err := r.Resolve(context.Background(), Peer{Name: "anything"})
	if !errors.Is(err, ErrResolverUnavailable) {
		t.Errorf("expected ErrResolverUnavailable for bad JSON; got %v", err)
	}
}

func TestTailscaleResolverBinaryAbsent(t *testing.T) {
	r := &TailscaleResolver{
		tailscaleBinary: "/nonexistent/path/to/tailscale",
		cacheTTL:        30 * time.Second,
	}
	if r.Available() {
		t.Error("Available should return false when binary is absent")
	}
}

func TestTailscaleResolverNameIsStable(t *testing.T) {
	r := NewTailscaleResolver()
	if r.Name() != "tailscale" {
		t.Errorf("Name = %q, want tailscale", r.Name())
	}
}

func TestTailscaleResolverHostnameFieldPreferredOverName(t *testing.T) {
	bin := makeStubTailscale(t, goodTailscaleStatus)
	r := &TailscaleResolver{tailscaleBinary: bin, cacheTTL: 30 * time.Second}

	// Peer.Hostname is the authoritative identifier; Peer.Name is
	// the human-readable label that may not match the network
	// hostname. When both are set and disagree, Hostname wins.
	addr, err := r.Resolve(context.Background(),
		Peer{Name: "human-label", Hostname: "peer-one"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if addr != "100.64.0.2" {
		t.Errorf("Resolve picked wrong field; got %q, want 100.64.0.2", addr)
	}
}

func TestTailscaleResolverCachesResults(t *testing.T) {
	// First Resolve forks the stub; subsequent Resolves within
	// cacheTTL should not. We can't directly assert the absence
	// of a fork (no observable side effect), but we can verify
	// the cache by changing the stub between calls and checking
	// the resolver returns the cached value.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tailscale")
	body := "#!/bin/sh\ncat <<'EOF'\n" + goodTailscaleStatus + "\nEOF\n"
	if err := os.WriteFile(binPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write initial stub: %v", err)
	}
	r := &TailscaleResolver{tailscaleBinary: binPath, cacheTTL: time.Hour}

	addr1, err := r.Resolve(context.Background(), Peer{Name: "peer-one"})
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Rewrite the stub to emit different data.
	emptyStatus := `{"Self":{"HostName":"self","TailscaleIPs":["100.64.0.1"]},"Peer":{}}`
	body2 := "#!/bin/sh\ncat <<'EOF'\n" + emptyStatus + "\nEOF\n"
	if err := os.WriteFile(binPath, []byte(body2), 0o755); err != nil {
		t.Fatalf("rewrite stub: %v", err)
	}

	addr2, err := r.Resolve(context.Background(), Peer{Name: "peer-one"})
	if err != nil {
		t.Fatalf("cached Resolve: %v", err)
	}
	if addr1 != addr2 {
		t.Errorf("cache invalidated unexpectedly: %q != %q", addr1, addr2)
	}
}

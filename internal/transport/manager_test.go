// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport is a Transport stub for manager tests. Behaviour is
// fully controlled by the test: which probes succeed, what latency
// they report, whether Available() returns true. No I/O.
type fakeTransport struct {
	name         string
	available    bool
	probeErr     error
	probeLatency time.Duration

	ensureCalls    atomic.Int32
	probeCalls     atomic.Int32
	propagateCalls atomic.Int32
}

func (f *fakeTransport) Name() string    { return f.name }
func (f *fakeTransport) Available() bool { return f.available }
func (f *fakeTransport) EnsurePeerReachability(_ context.Context, _ Folder, _ Peer) error {
	f.ensureCalls.Add(1)
	return nil
}
func (f *fakeTransport) RemovePeerReachability(_ context.Context, _ Folder, _ Peer) error { return nil }
func (f *fakeTransport) Probe(_ context.Context, _ Peer) (time.Duration, error) {
	f.probeCalls.Add(1)
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	return f.probeLatency, nil
}
func (f *fakeTransport) PropagateChange(_ context.Context, _ Change, _ Peer) error {
	f.propagateCalls.Add(1)
	return nil
}
func (f *fakeTransport) PropagatesSynchronously() bool { return true }

func TestDiscoverPopulatesRoutes(t *testing.T) {
	gitssh := &fakeTransport{name: "git-ssh+test", available: true, probeLatency: 5 * time.Millisecond}
	syncthing := &fakeTransport{name: "syncthing", available: true, probeLatency: 50 * time.Millisecond}
	m := NewManager([]Transport{gitssh, syncthing})

	peer := Peer{Name: "peer-a"}
	m.Discover(context.Background(), peer)

	routes, ok := m.RoutesFor("peer-a")
	if !ok {
		t.Fatal("RoutesFor returned ok=false after Discover")
	}
	if len(routes.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(routes.Entries))
	}
	if !routes.Entries["git-ssh+test"].Reachable {
		t.Error("git-ssh+test entry not reachable")
	}
	if !routes.Entries["syncthing"].Reachable {
		t.Error("syncthing entry not reachable")
	}
}

func TestDiscoverMarksUnavailableTransport(t *testing.T) {
	tailscale := &fakeTransport{name: "git-ssh+tailscale", available: false}
	syncthing := &fakeTransport{name: "syncthing", available: true, probeLatency: 50 * time.Millisecond}
	m := NewManager([]Transport{tailscale, syncthing})

	m.Discover(context.Background(), Peer{Name: "p"})

	routes, _ := m.RoutesFor("p")
	if routes.Entries["git-ssh+tailscale"].Reachable {
		t.Error("unavailable transport marked reachable")
	}
	if !routes.Entries["syncthing"].Reachable {
		t.Error("available transport not marked reachable")
	}
	if tailscale.probeCalls.Load() != 0 {
		t.Error("Probe called on unavailable transport")
	}
}

func TestDiscoverHandlesProbeError(t *testing.T) {
	failing := &fakeTransport{name: "git-ssh+test", available: true, probeErr: ErrUnreachable}
	working := &fakeTransport{name: "syncthing", available: true, probeLatency: 10 * time.Millisecond}
	m := NewManager([]Transport{failing, working})

	m.Discover(context.Background(), Peer{Name: "p"})

	routes, _ := m.RoutesFor("p")
	if routes.Entries["git-ssh+test"].Reachable {
		t.Error("transport with probe error marked reachable")
	}
	if !errors.Is(routes.Entries["git-ssh+test"].Err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", routes.Entries["git-ssh+test"].Err)
	}
}

func TestRouteReturnsErrNoRouteBeforeDiscover(t *testing.T) {
	m := NewManager([]Transport{&fakeTransport{name: "syncthing", available: true}})
	_, err := m.Route(Change{SizeHint: 1024}, "never-discovered")
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("expected ErrNoRoute before Discover; got %v", err)
	}
}

func TestRouteErrNoRouteWhenAllUnreachable(t *testing.T) {
	t1 := &fakeTransport{name: "t1", available: true, probeErr: ErrUnreachable}
	t2 := &fakeTransport{name: "t2", available: true, probeErr: ErrUnreachable}
	m := NewManager([]Transport{t1, t2})
	m.Discover(context.Background(), Peer{Name: "p"})

	_, err := m.Route(Change{SizeHint: 1024}, "p")
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("expected ErrNoRoute when all transports unreachable; got %v", err)
	}
}

func TestRoutePicksLowestPredictedCost(t *testing.T) {
	// gitssh: fast setup, slow throughput (good for small)
	gitssh := &fakeTransport{name: "git-ssh+test", available: true, probeLatency: 5 * time.Millisecond}
	// syncthing: slow setup, fast throughput (good for big)
	syncthing := &fakeTransport{name: "syncthing", available: true, probeLatency: 50 * time.Millisecond}

	m := NewManager([]Transport{gitssh, syncthing})
	m.Discover(context.Background(), Peer{Name: "p"})

	// Tiny payload — gitssh's lower setup wins.
	pick, err := m.Route(Change{SizeHint: 1024}, "p")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if pick.Name() != "git-ssh+test" {
		t.Errorf("for tiny payload, picked %s; want git-ssh+test", pick.Name())
	}

	// Huge payload — syncthing's higher throughput wins.
	pick, err = m.Route(Change{SizeHint: 100 * 1024 * 1024}, "p")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if pick.Name() != "syncthing" {
		t.Errorf("for huge payload, picked %s; want syncthing", pick.Name())
	}
}

func TestRouteAdaptsAfterRecordTransfer(t *testing.T) {
	// Start with default priors. The default git-ssh prior wins
	// for small payloads. Then RecordTransfer reports git-ssh
	// taking forever — the cost model should learn this and the
	// router should switch to syncthing.
	gitssh := &fakeTransport{name: "git-ssh+test", available: true, probeLatency: 5 * time.Millisecond}
	syncthing := &fakeTransport{name: "syncthing", available: true, probeLatency: 50 * time.Millisecond}
	m := NewManager([]Transport{gitssh, syncthing})
	m.Discover(context.Background(), Peer{Name: "p"})

	// Confirm baseline: small payload routes to gitssh.
	pick, _ := m.Route(Change{SizeHint: 1024}, "p")
	if pick.Name() != "git-ssh+test" {
		t.Fatalf("baseline expected git-ssh+test for 1KB, got %s", pick.Name())
	}

	// Feed observations showing gitssh is unexpectedly slow for
	// small payloads (e.g. peer is heavily loaded).
	for i := 0; i < 200; i++ {
		m.RecordTransfer("git-ssh+test", "p", int64(1024+i*100), 5*time.Second)
	}
	// And syncthing observed as fast.
	for i := 0; i < 200; i++ {
		m.RecordTransfer("syncthing", "p", int64(1024+i*100), 100*time.Millisecond)
	}

	pick, _ = m.Route(Change{SizeHint: 1024}, "p")
	if pick.Name() != "syncthing" {
		setupG, _, _ := m.ModelParametersFor("git-ssh+test", "p")
		setupS, _, _ := m.ModelParametersFor("syncthing", "p")
		t.Errorf("after observations, expected syncthing for 1KB; picked %s. gitssh setup=%.2f syncthing setup=%.2f",
			pick.Name(), setupG, setupS)
	}
}

func TestInvalidatePeerDropsRoutes(t *testing.T) {
	m := NewManager([]Transport{&fakeTransport{name: "syncthing", available: true, probeLatency: 10 * time.Millisecond}})
	m.Discover(context.Background(), Peer{Name: "p"})
	if _, ok := m.RoutesFor("p"); !ok {
		t.Fatal("Discover didn't populate routes")
	}
	m.InvalidatePeer("p")
	if _, ok := m.RoutesFor("p"); ok {
		t.Error("InvalidatePeer did not drop the entry")
	}
}

func TestTieBreakingFavoursEarlierTransport(t *testing.T) {
	// Two transports configured to have nearly identical
	// predictions. The earlier-listed one should win, providing
	// stability against noise.
	tA := &fakeTransport{name: "git-ssh+aaa", available: true, probeLatency: 10 * time.Millisecond}
	tB := &fakeTransport{name: "git-ssh+bbb", available: true, probeLatency: 10 * time.Millisecond}
	m := NewManager([]Transport{tA, tB})
	m.Discover(context.Background(), Peer{Name: "p"})

	// Both have identical default priors → identical predictions.
	// Earlier one (aaa) wins.
	pick, _ := m.Route(Change{SizeHint: 1024}, "p")
	if pick.Name() != "git-ssh+aaa" {
		t.Errorf("on tie, expected first-listed git-ssh+aaa; got %s", pick.Name())
	}
}

func TestRecordTransferIsolatedPerTransportAndPeer(t *testing.T) {
	m := NewManager([]Transport{
		&fakeTransport{name: "t1", available: true, probeLatency: 5 * time.Millisecond},
		&fakeTransport{name: "t2", available: true, probeLatency: 5 * time.Millisecond},
	})

	// Recording on (t1, peer-a) must not affect (t1, peer-b) or (t2, peer-a).
	m.RecordTransfer("t1", "peer-a", 1024, 1*time.Second)

	_, _, nA := m.ModelParametersFor("t1", "peer-a")
	_, _, nB := m.ModelParametersFor("t1", "peer-b")
	_, _, nC := m.ModelParametersFor("t2", "peer-a")

	if nA == 0 {
		t.Error("(t1, peer-a) should have one observation")
	}
	if nB != 0 {
		t.Errorf("(t1, peer-b) should have zero observations; got %.2f", nB)
	}
	if nC != 0 {
		t.Errorf("(t2, peer-a) should have zero observations; got %.2f", nC)
	}
}

// TestRecordTransferAcceptsUnknownTransportName pins that the
// Manager doesn't panic when RecordTransfer is called for a
// transport name that wasn't registered at construction. The
// realistic scenario: a propagator built on top of a Manager
// passes through a name from an external source (config, CLI
// arg). The current contract is "create a model on first use with
// the DefaultPriorFor fallback so the observation is absorbed
// rather than dropped." Future refactors that tighten this (e.g.
// reject unknown names) need an explicit decision and an updated
// test.
func TestRecordTransferAcceptsUnknownTransportName(t *testing.T) {
	m := NewManager([]Transport{
		&fakeTransport{name: "t1", available: true, probeLatency: 5 * time.Millisecond},
	})

	// Record under a name that was never registered. Must not
	// panic and must absorb the observation under a fresh model.
	m.RecordTransfer("never-registered", "peer-a", 1024, 1*time.Second)

	_, _, n := m.ModelParametersFor("never-registered", "peer-a")
	if n == 0 {
		t.Error("RecordTransfer with unknown transport name silently dropped the observation; effective-sample count = 0")
	}
}

// TestDiscoverWithCancelledContextStillCompletes pins that
// Discover doesn't panic, deadlock, or corrupt route state when
// the caller's context is already cancelled. In production this
// path fires on suspend-then-resume races: Discover is invoked
// from a context that was created before suspend and is dead by
// the time the resume handler runs. The function must produce a
// well-formed (probably-unreachable) Routes entry rather than
// silently abort, so the next Discover from a live context can
// repopulate cleanly.
func TestDiscoverWithCancelledContextStillCompletes(t *testing.T) {
	m := NewManager([]Transport{
		&fakeTransport{name: "t1", available: true, probeLatency: 5 * time.Millisecond},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: every probe sees a dead context.

	// Must return, not block. A bounded retry of up to 1s would
	// be acceptable; what we're protecting against is a hang.
	done := make(chan struct{})
	go func() {
		m.Discover(ctx, Peer{Name: "suspended"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Discover did not return within 2s on cancelled ctx — likely deadlocked")
	}

	// Route table must be well-formed (entry exists for each
	// transport) so a subsequent live-context Discover can
	// overwrite it without nil-map panics.
	routes, ok := m.RoutesFor("suspended")
	if !ok {
		t.Fatal("RoutesFor returned ok=false; Discover should populate the table even with cancelled ctx")
	}
	if _, hasEntry := routes.Entries["t1"]; !hasEntry {
		t.Errorf("routes.Entries missing t1; got %v", routes.Entries)
	}
}

func TestDiscoverConcurrencySafe(t *testing.T) {
	// Stress test: many goroutines calling Discover for the same
	// peer simultaneously must not panic, race, or corrupt state.
	m := NewManager([]Transport{
		&fakeTransport{name: "t1", available: true, probeLatency: time.Millisecond},
		&fakeTransport{name: "t2", available: true, probeLatency: time.Millisecond},
	})
	const N = 50
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			m.Discover(context.Background(), Peer{Name: "p"})
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
	// Final state must have exactly two reachable entries.
	routes, _ := m.RoutesFor("p")
	if len(routes.Entries) != 2 {
		t.Errorf("after concurrent Discover, got %d entries; want 2", len(routes.Entries))
	}
}

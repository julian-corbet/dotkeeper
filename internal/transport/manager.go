// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Manager is the v1.0.0 multi-transport orchestrator. It owns:
//
//  1. A static set of Transport implementations supplied at
//     construction.
//  2. A per-peer Routes table populated by Discover and refreshed
//     only on explicit events (peer paired, wake from suspend,
//     hard failure on a transport, operator-issued rediscover).
//     No periodic probing — connectivity between paired hosts is
//     topology, which doesn't change every five minutes.
//  3. A per-(transport, peer) CostModel that learns from observed
//     transfer durations. Updated by RecordTransfer after every
//     successful PropagateChange.
//
// The Manager exposes one routing decision: Route(change, peerName)
// picks the Transport with the lowest predicted cost for the change's
// size. Returns ErrNoRoute when no transport can reach the peer at
// all.
//
// Discovery and routing are decoupled: Discover answers "what's
// possible," Route answers "what's optimal right now." Both are
// independent of any wall-clock cadence.
type Manager struct {
	transports []Transport

	mu          sync.RWMutex
	routes      map[string]Routes      // peerName -> reachability snapshot
	models      map[modelKey]*CostModel // (transportName, peerName) -> cost model
	priorsByTpt map[string]CostPrior   // transport.Name() -> prior, used when constructing new models
}

// CostPrior captures the bootstrap parameters for a single Transport's
// CostModel. Each Transport implementation should supply a sensible
// default; operators can override via configuration in v1.1+.
type CostPrior struct {
	SetupMS     float64
	MSPerByte   float64
	Weight      float64
	HalfLifeSec float64
}

// DefaultPriorFor returns reasonable bootstrap parameters for a
// transport identified by name. Used when no operator-supplied
// override exists. Numbers are based on observed real-world
// behaviour of the relevant transports under typical conditions.
//
// Picking priors is a judgment call: too aggressive and the
// first-decision routing is wrong (transport that never works gets
// picked because its prior was over-optimistic); too pessimistic
// and the model takes longer to discover good transports. The
// values below err toward conservative — initial routing will
// favour transports that have historically been reliable.
func DefaultPriorFor(transportName string) CostPrior {
	switch transportName {
	case "syncthing":
		return CostPrior{
			SetupMS:     1500,    // BEP gossip + block scheduling
			MSPerByte:   0.00002, // ~50 MB/s effective on LAN
			Weight:      4,
			HalfLifeSec: 86400, // 1 day
		}
	default:
		// Best-effort default for any git-ssh+* variant or
		// future transport. Reflects "fast setup, modest
		// throughput" — typical for shelled-out CLI tools that
		// authenticate once and stream.
		return CostPrior{
			SetupMS:     200,
			MSPerByte:   0.0002, // ~5 MB/s
			Weight:      4,
			HalfLifeSec: 86400,
		}
	}
}

// modelKey is the lookup key for the per-(transport, peer) cost
// model map. Tuple key because each transport observes its own
// throughput against each peer independently.
type modelKey struct {
	transport string
	peer      string
}

// Routes is the per-peer reachability snapshot recorded by Discover.
// Keys are transport.Name(); values are the resolved details. The
// snapshot is point-in-time as of LastUpdated.
type Routes struct {
	Entries     map[string]RouteEntry // transport.Name() -> entry
	LastUpdated time.Time
}

// RouteEntry is one row in the Routes table.
type RouteEntry struct {
	// Reachable reports whether the most recent discovery probe
	// for this transport+peer succeeded. Unreachable transports
	// stay in the map (so the CLI can show them) but Route skips
	// them.
	Reachable bool

	// ProbeLatency is the latency measured at the most recent
	// Discover call. Not the same thing as Predict(0) — Predict
	// returns the cost-model's setup estimate, which incorporates
	// observed transfers; ProbeLatency is the one-shot probe RTT.
	// Used by the CLI to show "what discovery saw" alongside the
	// cost model's evolved prediction.
	ProbeLatency time.Duration

	// Err is the error from the most recent probe, if any.
	// Reachable=false implies Err!=nil.
	Err error
}

// NewManager constructs a Manager. Each transport's prior cost
// model is seeded from DefaultPriorFor(transport.Name()); v1.1+
// will load operator overrides from config.
func NewManager(transports []Transport) *Manager {
	priorsByTpt := make(map[string]CostPrior, len(transports))
	for _, t := range transports {
		priorsByTpt[t.Name()] = DefaultPriorFor(t.Name())
	}
	return &Manager{
		transports:  transports,
		routes:      make(map[string]Routes),
		models:      make(map[modelKey]*CostModel),
		priorsByTpt: priorsByTpt,
	}
}

// Transports returns the manager's configured transport list.
// Includes transports that are currently Unavailable; the CLI uses
// this to show "what could be configured" not just "what's live."
func (m *Manager) Transports() []Transport {
	out := make([]Transport, len(m.transports))
	copy(out, m.transports)
	return out
}

// Discover runs reachability probes for every Available transport
// against the given peer and updates the Routes table. Idempotent:
// re-calling overwrites the previous snapshot. Returns once every
// probe has completed (or hit its per-transport timeout, which the
// Transport implementation enforces internally).
//
// Probes run concurrently — a slow Tailscale lookup doesn't delay
// the parallel Syncthing ping. Total wall-clock latency of Discover
// is the slowest probe's individual timeout.
//
// Discover is the only operation that does network I/O against
// peers from inside the Manager; Route is pure compute against the
// cached state. This separation makes the routing path itself
// trivially testable.
func (m *Manager) Discover(ctx context.Context, peer Peer) {
	type result struct {
		name      string
		reachable bool
		latency   time.Duration
		err       error
	}
	results := make(chan result, len(m.transports))
	var wg sync.WaitGroup
	for _, t := range m.transports {
		if !t.Available() {
			results <- result{name: t.Name(), reachable: false, err: errors.New("transport unavailable on this host")}
			continue
		}
		wg.Add(1)
		go func(t Transport) {
			defer wg.Done()
			d, err := t.Probe(ctx, peer)
			results <- result{name: t.Name(), reachable: err == nil, latency: d, err: err}
		}(t)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	entries := make(map[string]RouteEntry, len(m.transports))
	for r := range results {
		entries[r.name] = RouteEntry{
			Reachable:    r.reachable,
			ProbeLatency: r.latency,
			Err:          r.err,
		}
	}

	m.mu.Lock()
	m.routes[peer.Name] = Routes{Entries: entries, LastUpdated: time.Now()}
	m.mu.Unlock()
}

// InvalidatePeer drops the Routes entry for peer. Forces the next
// Route call to fall back to "no information" defaults until the
// caller fires Discover. Used by the daemon on wake-from-suspend
// (network may have changed) and on explicit operator request.
func (m *Manager) InvalidatePeer(peerName string) {
	m.mu.Lock()
	delete(m.routes, peerName)
	m.mu.Unlock()
}

// Route picks the Transport with the lowest predicted cost for the
// change's size, among transports that are currently reachable for
// peer. Returns ErrNoRoute when discovery has never run for the
// peer or no transport is reachable.
//
// Routing is pure compute: it consults the Routes table (last
// known reachability), the per-(transport, peer) CostModels (their
// current parameter fit), and the change's SizeHint. No network
// I/O. The decision takes microseconds and is safe to call from
// the hot path of reconcile.
//
// Tie-breaking: when two transports' Predict outputs are within 1ms
// of each other, the one that appears earlier in the manager's
// transport list wins. Operators can rely on this stability by
// ordering the transport list deliberately at construction (the
// daemon orders Syncthing last so the default is "use the
// alternative when it's at least as good," not "flip back and
// forth on every-tick noise").
func (m *Manager) Route(change Change, peerName string) (Transport, error) {
	m.mu.RLock()
	routes, ok := m.routes[peerName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: no Discover has run for peer %q", ErrNoRoute, peerName)
	}

	type candidate struct {
		t       Transport
		predict time.Duration
	}
	var candidates []candidate
	for _, t := range m.transports {
		entry, found := routes.Entries[t.Name()]
		if !found || !entry.Reachable {
			continue
		}
		model := m.modelFor(t.Name(), peerName)
		predict := model.Predict(change.SizeHint)
		candidates = append(candidates, candidate{t: t, predict: predict})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no reachable transport for peer %q", ErrNoRoute, peerName)
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		// 1ms tie-breaking band — predictions within this band
		// are treated as equivalent; the earlier-listed transport
		// wins. Prevents flapping on noise.
		if c.predict+time.Millisecond < best.predict {
			best = c
		}
	}
	return best.t, nil
}

// RecordTransfer feeds an observed transfer back into the cost model
// for the (transport, peer) pair. Called by the daemon after each
// successful PropagateChange. Failed transfers do not call this —
// they're noise, not signal.
//
// Cheap: O(1) regression update. Safe to call from the hot path
// where the transfer just completed.
func (m *Manager) RecordTransfer(transportName, peerName string, sizeBytes int64, elapsed time.Duration) {
	m.modelFor(transportName, peerName).Record(sizeBytes, elapsed)
}

// modelFor returns the cost model for the (transport, peer) pair,
// creating it on first use. Thread-safe via the manager's mutex.
func (m *Manager) modelFor(transportName, peerName string) *CostModel {
	key := modelKey{transport: transportName, peer: peerName}
	m.mu.RLock()
	if model, ok := m.models[key]; ok {
		m.mu.RUnlock()
		return model
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if model, ok := m.models[key]; ok { // re-check under write lock
		return model
	}
	prior := m.priorsByTpt[transportName]
	if prior.Weight == 0 {
		prior = DefaultPriorFor(transportName)
	}
	model := NewCostModel(prior.SetupMS, prior.MSPerByte, prior.Weight, prior.HalfLifeSec)
	m.models[key] = model
	return model
}

// RoutesFor returns a snapshot of the discovered routes for peer.
// Used by the CLI; a defensive copy so the caller can iterate
// without holding the manager's lock.
func (m *Manager) RoutesFor(peerName string) (Routes, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.routes[peerName]
	if !ok {
		return Routes{}, false
	}
	entries := make(map[string]RouteEntry, len(r.Entries))
	for k, v := range r.Entries {
		entries[k] = v
	}
	return Routes{Entries: entries, LastUpdated: r.LastUpdated}, true
}

// ModelParametersFor returns the current (setup, msPerByte, n) for
// the named (transport, peer) cost model. Used by the CLI for
// "show me the learned parameters" output. Creates the model if it
// doesn't exist yet (returns the prior values).
func (m *Manager) ModelParametersFor(transportName, peerName string) (setupMS, msPerByte, effectiveSamples float64) {
	return m.modelFor(transportName, peerName).Parameters()
}

// ErrNoRoute indicates Route was called for a peer with no
// reachable transport. Callers should fall back to "try Discover
// first" then surface the error if it persists.
var ErrNoRoute = errors.New("no route to peer")

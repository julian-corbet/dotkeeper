// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"math"
	"sync"
	"time"
)

// CostModel predicts how long a transport will take to carry a
// payload of a given size to a given peer. The model is the linear
// equation:
//
//	cost(bytes) = setupMS + bytes * msPerByte
//
// where setupMS is the constant overhead (SSH handshake, gossip
// propagation delay, BEP negotiation) and msPerByte is the
// inverse of throughput. Both parameters are learned online from
// observed transfers.
//
// Why linear? Real-world transfer time is dominated by setup +
// bandwidth-limited transfer for the size range dotkeeper carries
// (kilobytes to gigabytes). At extreme sizes (>10s of GB) the
// linear approximation breaks down due to retransmits, congestion
// control, kernel buffer effects — but dotkeeper isn't moving
// hundred-gigabyte files in single transactions. For that regime
// the operator should switch to dedicated tooling.
//
// Why online linear regression rather than per-sample averaging?
// Single-transfer time is noisy (kernel scheduling, peer load,
// network microbursts). Regression with multiple observations at
// different sizes recovers the underlying setup vs throughput
// split, which is what the router actually needs to make
// crossover decisions. Per-sample averaging would conflate them.
//
// Exponential decay on older observations means the model adapts
// to changing conditions (network rerouted, peer load shifted)
// without manual reset. Half-life is configurable; default
// chosen so the model "forgets" the first sample after about a
// day of continuous use — long enough that one bad sample doesn't
// dominate, short enough that genuine network changes are reflected
// within hours.
type CostModel struct {
	// Prior parameters provide a sensible starting point before
	// any observation has been recorded. The router picks a
	// transport even on first use; the priors determine which one
	// wins until data accumulates. Each transport implementation
	// supplies its own priors based on typical real-world
	// performance — SSH+git is fast-setup-slow-throughput,
	// Syncthing is slow-setup-fast-throughput.
	priorSetupMS    float64
	priorMSPerByte  float64
	priorWeight     float64 // pseudo-count of synthetic observations

	// Decay half-life in seconds. Older observations contribute
	// less weight to the current fit.
	halfLifeSec float64

	// Mutable state, guarded by mu.
	mu             sync.Mutex
	weightedN      float64 // effective sample count after decay
	weightedSumX   float64 // sum of weight * x (where x = size in bytes)
	weightedSumY   float64 // sum of weight * y (where y = observed ms)
	weightedSumXX  float64 // sum of weight * x^2
	weightedSumXY  float64 // sum of weight * x*y
	lastUpdate     time.Time

	// Cached fitted parameters; recomputed after each Record.
	// Reading Predict() takes the lock, evaluates from cache.
	fittedSetupMS   float64
	fittedMSPerByte float64
}

// NewCostModel returns a model seeded with prior parameters. The
// prior is treated as `priorWeight` synthetic observations evenly
// distributed across small and medium sizes, which gives the model
// a defensible starting prediction before any real data arrives.
// As real observations come in, their weight grows and the prior's
// influence shrinks proportionally.
//
// priorSetupMS: typical small-payload time (size ~ 0). Examples:
//   - git+ssh over Tailscale: ~150ms (SSH handshake + branch ref)
//   - Syncthing local LAN: ~1500ms (BEP gossip + block scheduling)
//
// priorMSPerByte: 1 / (bytes/ms throughput). Examples:
//   - git+ssh: 1/5000 = 0.0002 ms/byte (~5 MB/s effective)
//   - Syncthing LAN: 1/50000 = 0.00002 ms/byte (~50 MB/s)
//
// priorWeight: how strongly the prior anchors the fit. 8 means
// "the prior is worth 8 synthetic observations." Real observations
// outweigh the prior after ~16 transfers (depending on size mix).
//
// halfLifeSec controls how fast old observations decay. 86400 (one
// day) is the v1.0.0 default — long enough that intermittent noise
// doesn't dominate, short enough that genuine condition changes
// (network rerouted, host upgraded) reach the model within hours.
func NewCostModel(priorSetupMS, priorMSPerByte, priorWeight, halfLifeSec float64) *CostModel {
	m := &CostModel{
		priorSetupMS:   priorSetupMS,
		priorMSPerByte: priorMSPerByte,
		priorWeight:    priorWeight,
		halfLifeSec:    halfLifeSec,
		lastUpdate:     time.Now(),
	}
	m.fittedSetupMS = priorSetupMS
	m.fittedMSPerByte = priorMSPerByte
	return m
}

// Predict returns the model's best estimate of how long a transfer
// of sizeBytes will take. Always returns a non-negative duration;
// negative coefficients (from pathological data) are clamped at
// the prior values rather than producing nonsense predictions.
//
// Pure read operation — safe to call from any goroutine.
func (m *CostModel) Predict(sizeBytes int64) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	setup := m.fittedSetupMS
	perByte := m.fittedMSPerByte
	if setup < 0 {
		setup = m.priorSetupMS
	}
	if perByte < 0 {
		perByte = m.priorMSPerByte
	}
	ms := setup + float64(sizeBytes)*perByte
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// Record incorporates one observed (sizeBytes, elapsed) sample
// into the model. Updates the running weighted regression in
// O(1); the fit is recomputed inline and cached for the next
// Predict call.
//
// Observations with sizeBytes < 0 or elapsed < 0 are dropped — they
// represent caller bugs we'd rather not propagate into the fit.
// elapsed == 0 is permitted (a degenerate "instantaneous" reply
// from a cached SSH connection) and contributes normally.
//
// Decay: observations earlier than the previous Record are scaled
// down by `exp(-Δt * ln(2) / halfLife)`. Implemented by scaling the
// running sums every time Record is called, which keeps the most
// recent sample at full weight and gradually erodes older ones.
func (m *CostModel) Record(sizeBytes int64, elapsed time.Duration) {
	if sizeBytes < 0 || elapsed < 0 {
		return
	}
	x := float64(sizeBytes)
	// Sub-millisecond precision matters for small payloads where
	// the entire transfer might land in 50.5ms; truncating to
	// integer milliseconds (elapsed.Milliseconds()) loses ~1ms of
	// resolution per sample, which compounds across observations
	// and biases the fit downward. Use Nanoseconds/1e6 instead.
	y := float64(elapsed.Nanoseconds()) / 1e6

	m.mu.Lock()
	defer m.mu.Unlock()

	// Apply decay to existing running sums before adding the new
	// sample. The new sample has weight 1 and lands at "now."
	now := time.Now()
	if !m.lastUpdate.IsZero() {
		dt := now.Sub(m.lastUpdate).Seconds()
		if dt > 0 && m.halfLifeSec > 0 {
			decay := math.Exp(-dt * math.Ln2 / m.halfLifeSec)
			m.weightedN *= decay
			m.weightedSumX *= decay
			m.weightedSumY *= decay
			m.weightedSumXX *= decay
			m.weightedSumXY *= decay
		}
	}
	m.lastUpdate = now

	m.weightedN += 1
	m.weightedSumX += x
	m.weightedSumY += y
	m.weightedSumXX += x * x
	m.weightedSumXY += x * y

	m.refit()
}

// refit computes the regression coefficients from real observations
// and blends them with the prior. Called from Record under the lock.
//
// Approach: compute least-squares from the running weighted sums
// (real observations only), then take a weighted average of the
// real-data fit and the prior parameters. The blend weight is
// `realN / (realN + priorWeight)` — when real observations are few,
// the prior dominates; as real observations accumulate, the prior
// fades smoothly.
//
// This is cleaner than synthesising pseudo-observations and feeding
// them into the regression: synthetic points with x far from the
// real-data range have disproportionate leverage and can push the
// fit through nonsense. The blend approach treats prior and data
// as separate sources of information, which is what they are.
//
// Edge cases:
//
//   - n < 2 real observations: slope is undefined. Use prior.
//   - Zero variance in x (all observations at the same size):
//     denominator is zero. Use prior — we don't have enough
//     diversity to recover slope.
//   - Real fit produces nonsense (NaN, Inf): use prior. Defensive
//     because pathological observation sequences (single sample at
//     x=0, etc.) can produce undefined arithmetic in the OLS
//     formula.
func (m *CostModel) refit() {
	if m.weightedN < 2 {
		m.fittedSetupMS = m.priorSetupMS
		m.fittedMSPerByte = m.priorMSPerByte
		return
	}
	n := m.weightedN
	sumX := m.weightedSumX
	sumY := m.weightedSumY
	sumXX := m.weightedSumXX
	sumXY := m.weightedSumXY

	denom := n*sumXX - sumX*sumX
	if denom <= 0 {
		m.fittedSetupMS = m.priorSetupMS
		m.fittedMSPerByte = m.priorMSPerByte
		return
	}
	realSlope := (n*sumXY - sumX*sumY) / denom
	realIntercept := (sumY - realSlope*sumX) / n
	if math.IsNaN(realSlope) || math.IsInf(realSlope, 0) ||
		math.IsNaN(realIntercept) || math.IsInf(realIntercept, 0) {
		m.fittedSetupMS = m.priorSetupMS
		m.fittedMSPerByte = m.priorMSPerByte
		return
	}

	// Bayesian-ish blend. priorWeight is in units of "equivalent
	// observations" so the formula reads as a weighted average.
	total := n + m.priorWeight
	m.fittedSetupMS = (n*realIntercept + m.priorWeight*m.priorSetupMS) / total
	m.fittedMSPerByte = (n*realSlope + m.priorWeight*m.priorMSPerByte) / total
}

// Parameters returns the current fitted model parameters along with
// the effective sample count (after decay). Used by CLI commands
// and tests that inspect model state. The returned values are a
// point-in-time snapshot; the model may continue updating
// asynchronously.
func (m *CostModel) Parameters() (setupMS, msPerByte, effectiveSamples float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fittedSetupMS, m.fittedMSPerByte, m.weightedN
}

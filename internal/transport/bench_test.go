// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"testing"
	"time"
)

// Benchmarks for the transport-layer hot paths.
//
// These exist to catch silent perf regressions: a future refactor
// that adds a syscall or an O(n) loop to a function called on every
// commit will show up as a 100× number here long before it shows up
// in production CPU graphs.
//
// Target order-of-magnitude (Intel-class laptop, 2026 baselines):
//
//	BenchmarkCostModelPredict      <100 ns/op
//	BenchmarkManagerRoute          <1   µs/op (small payload, 2 transports)
//	BenchmarkManagerRecordTransfer <500 ns/op
//
// Run with: go test -tags noassets -bench=. -benchtime=2s ./internal/transport/

func BenchmarkCostModelPredict(b *testing.B) {
	m := NewCostModel(200, 0.0002, 4, 86400)
	// Feed a few observations so Predict goes through the fitted
	// path rather than the prior-only short-circuit.
	for i := 0; i < 16; i++ {
		m.Record(int64(1024*(i+1)), time.Duration(50+i)*time.Millisecond)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Predict(int64(1024 * (i % 32)))
	}
}

func BenchmarkManagerRouteTwoTransports(b *testing.B) {
	gitssh := &fakeTransport{name: "git-ssh+test", available: true, probeLatency: 5 * time.Millisecond}
	syncthing := &fakeTransport{name: "syncthing", available: true, probeLatency: 50 * time.Millisecond}
	m := NewManager([]Transport{gitssh, syncthing})
	m.Discover(context.Background(), Peer{Name: "p"})
	change := Change{SizeHint: 4096}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.Route(change, "p")
	}
}

func BenchmarkManagerRecordTransfer(b *testing.B) {
	m := NewManager([]Transport{
		&fakeTransport{name: "git-ssh+test", available: true, probeLatency: 5 * time.Millisecond},
	})
	m.Discover(context.Background(), Peer{Name: "p"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.RecordTransfer("git-ssh+test", "p", 4096, 50*time.Millisecond)
	}
}

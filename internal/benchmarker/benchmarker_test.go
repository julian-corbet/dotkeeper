// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package benchmarker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/transport"
)

// stubRecorder implements Recorder with controllable model state +
// observation log. No network or filesystem.
type stubRecorder struct {
	mu              sync.Mutex
	observations    []recordedObservation
	modelSamples    map[string]float64 // key="t|p|r" → effective samples
	routes          map[string]transport.Routes
	transportsValue []transport.Transport
}

type recordedObservation struct {
	transport string
	peer      string
	repo      string
	bytes     int64
	elapsed   time.Duration
}

func newStubRecorder() *stubRecorder {
	return &stubRecorder{
		modelSamples: map[string]float64{},
		routes:       map[string]transport.Routes{},
	}
}

func (s *stubRecorder) RecordTransfer(t, p, r string, bytes int64, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, recordedObservation{t, p, r, bytes, elapsed})
}

func (s *stubRecorder) ModelParametersFor(t, p, r string) (float64, float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return 0, 0, s.modelSamples[t+"|"+p+"|"+r]
}

func (s *stubRecorder) RoutesFor(peer string) (transport.Routes, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routes[peer]
	return r, ok
}

func (s *stubRecorder) Transports() []transport.Transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transportsValue
}

func (s *stubRecorder) setModelSamples(t, p, r string, n float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelSamples[t+"|"+p+"|"+r] = n
}

func (s *stubRecorder) observationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.observations)
}

// fakeTransport satisfies the transport.Transport interface for
// benchmarker tests. Records propagate calls and lets the test set
// per-call latency + error.
type fakeTransport struct {
	name string
	sync bool

	propagateMu  sync.Mutex
	propagateErr error
	propagateLat time.Duration
	propagateN   atomic.Int32
}

func (f *fakeTransport) Name() string    { return f.name }
func (f *fakeTransport) Available() bool { return true }
func (f *fakeTransport) EnsurePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (f *fakeTransport) RemovePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (f *fakeTransport) Probe(_ context.Context, _ transport.Peer) (time.Duration, error) {
	return 0, nil
}
func (f *fakeTransport) PropagateChange(_ context.Context, _ transport.Change, _ transport.Peer) error {
	f.propagateN.Add(1)
	f.propagateMu.Lock()
	lat := f.propagateLat
	err := f.propagateErr
	f.propagateMu.Unlock()
	if lat > 0 {
		time.Sleep(lat)
	}
	return err
}
func (f *fakeTransport) PropagatesSynchronously() bool { return f.sync }

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestRunOnceBenchmarksReachableSyncTransports verifies the
// happy-path scan: a reachable synchronous transport for a known
// peer + folder gets benchmarked, one observation is recorded, and
// the probe file is cleaned up.
func TestRunOnceBenchmarksReachableSyncTransports(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	syncTransport := &fakeTransport{name: "mutagen+test", sync: true, propagateLat: 5 * time.Millisecond}
	rec.transportsValue = []transport.Transport{syncTransport}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{
			"mutagen+test": {Reachable: true},
		},
	}

	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{})

	b.runOnce(context.Background())

	if syncTransport.propagateN.Load() != 1 {
		t.Errorf("expected 1 PropagateChange call, got %d", syncTransport.propagateN.Load())
	}
	if got := rec.observationCount(); got != 1 {
		t.Errorf("expected 1 recorded observation, got %d", got)
	}
	// Cleanup: probe dir should be empty (and ideally removed —
	// the defer also tries `os.Remove` on the dir).
	probeDir := filepath.Join(tmpFolder, ProbeSubdir)
	entries, err := os.ReadDir(probeDir)
	if err == nil && len(entries) != 0 {
		t.Errorf("probe dir still contains %d files", len(entries))
	}
}

// TestRunOnceSkipsAsyncTransports — Syncthing-style transports
// (PropagatesSynchronously=false) must never be benchmarked
// because their µs-scale no-op duration would teach the cost
// model that they're infinitely fast.
func TestRunOnceSkipsAsyncTransports(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	asyncT := &fakeTransport{name: "syncthing", sync: false}
	rec.transportsValue = []transport.Transport{asyncT}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{"syncthing": {Reachable: true}},
	}

	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{})

	b.runOnce(context.Background())
	if asyncT.propagateN.Load() != 0 {
		t.Errorf("async transport must not be benchmarked; got %d propagate calls",
			asyncT.propagateN.Load())
	}
	if got := rec.observationCount(); got != 0 {
		t.Errorf("expected 0 observations for async-only fleet; got %d", got)
	}
}

// TestRunOnceSkipsUnreachableTransports — Discover's reachability
// snapshot gates benchmarking. An unreachable transport must not
// be probed (would just time out).
func TestRunOnceSkipsUnreachableTransports(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	syncT := &fakeTransport{name: "mutagen+test", sync: true}
	rec.transportsValue = []transport.Transport{syncT}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{
			"mutagen+test": {Reachable: false, Err: errors.New("ssh refused")},
		},
	}

	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{})
	b.runOnce(context.Background())
	if syncT.propagateN.Load() != 0 {
		t.Errorf("unreachable transport must not be probed; got %d", syncT.propagateN.Load())
	}
}

// TestRunOnceRespectsCadence — running benchmark twice in
// succession must only execute the first; the second tick falls
// under the cadence gate and skips.
func TestRunOnceRespectsCadence(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	syncT := &fakeTransport{name: "mutagen+test", sync: true}
	rec.transportsValue = []transport.Transport{syncT}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{"mutagen+test": {Reachable: true}},
	}

	now := time.Now()
	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{
			Cadence: time.Hour,
			NowFn:   func() time.Time { return now },
		})

	b.runOnce(context.Background())
	b.runOnce(context.Background())

	if syncT.propagateN.Load() != 1 {
		t.Errorf("cadence gate failed; expected 1 benchmark per cadence, got %d",
			syncT.propagateN.Load())
	}
}

// TestRunOnceSkipsConvergedModel — once a tuple has the configured
// number of effective samples, the benchmark stops running
// (organic traffic is keeping it accurate).
func TestRunOnceSkipsConvergedModel(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	syncT := &fakeTransport{name: "mutagen+test", sync: true}
	rec.transportsValue = []transport.Transport{syncT}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{"mutagen+test": {Reachable: true}},
	}
	// Model is already well-fit: 50 effective samples >> 20 threshold.
	rec.setModelSamples("mutagen+test", "peer-a", "dk-x", 50)

	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{})

	b.runOnce(context.Background())
	if syncT.propagateN.Load() != 0 {
		t.Errorf("converged model must skip benchmark; got %d", syncT.propagateN.Load())
	}
}

// TestBenchmarkOneCleansUpOnPropagateError — even when
// PropagateChange fails, the probe file and dir must be removed.
// Without this, a transport that consistently errors would
// progressively pollute the user's folders.
func TestBenchmarkOneCleansUpOnPropagateError(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	failingT := &fakeTransport{
		name: "mutagen+test", sync: true,
		propagateErr: errors.New("simulated peer error"),
	}
	rec.transportsValue = []transport.Transport{failingT}
	b := New(rec,
		func() []transport.Folder { return nil },
		func() []transport.Peer { return nil },
		silentLogger(),
		Options{})

	err := b.benchmarkOne(context.Background(), failingT,
		transport.Peer{Name: "peer-a"},
		transport.Folder{ID: "dk-x", Path: tmpFolder},
		time.Now())

	if err == nil {
		t.Fatal("expected PropagateChange error to surface")
	}
	probeDir := filepath.Join(tmpFolder, ProbeSubdir)
	entries, err := os.ReadDir(probeDir)
	if err == nil && len(entries) != 0 {
		t.Errorf("probe dir not cleaned after error; %d files remain", len(entries))
	}
	// And the observation must NOT be recorded — failed transfers
	// are noise, not signal.
	if got := rec.observationCount(); got != 0 {
		t.Errorf("failed probe wrongly recorded an observation; got %d", got)
	}
}

// TestRunHonorsContextCancellation — daemon shutdown semantics:
// cancelling the parent ctx must return Run promptly.
func TestRunHonorsContextCancellation(t *testing.T) {
	rec := newStubRecorder()
	b := New(rec,
		func() []transport.Folder { return nil },
		func() []transport.Peer { return nil },
		silentLogger(),
		Options{TickInterval: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}

// TestBenchmarkNowReportsPerTuple — the operator-triggered CLI
// path returns one ProbeResult per (transport, peer) pair, with
// errors surfaced individually so the CLI can render a complete
// picture even when some probes fail.
func TestBenchmarkNowReportsPerTuple(t *testing.T) {
	tmpFolder := t.TempDir()
	rec := newStubRecorder()
	syncOK := &fakeTransport{name: "mutagen+test", sync: true}
	syncFail := &fakeTransport{name: "git-ssh+test", sync: true, propagateErr: errors.New("boom")}
	rec.transportsValue = []transport.Transport{syncOK, syncFail}
	rec.routes["peer-a"] = transport.Routes{
		Entries: map[string]transport.RouteEntry{
			"mutagen+test": {Reachable: true},
			"git-ssh+test": {Reachable: true},
		},
	}
	b := New(rec,
		func() []transport.Folder { return []transport.Folder{{ID: "dk-x", Path: tmpFolder}} },
		func() []transport.Peer { return []transport.Peer{{Name: "peer-a"}} },
		silentLogger(),
		Options{})

	results := b.BenchmarkNow(context.Background(),
		transport.Folder{ID: "dk-x", Path: tmpFolder})

	if len(results) != 2 {
		t.Fatalf("expected 2 results (one per sync transport), got %d", len(results))
	}
	var sawOK, sawErr bool
	for _, r := range results {
		switch r.Transport {
		case "mutagen+test":
			if r.Err != nil {
				t.Errorf("mutagen+test should have succeeded, got err=%v", r.Err)
			}
			sawOK = true
		case "git-ssh+test":
			if r.Err == nil {
				t.Errorf("git-ssh+test should have errored")
			}
			sawErr = true
		}
	}
	if !sawOK || !sawErr {
		t.Errorf("missing expected result entries (ok=%v err=%v)", sawOK, sawErr)
	}
}

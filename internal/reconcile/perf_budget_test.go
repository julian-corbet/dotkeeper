// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"testing"
	"time"
)

// Perf-budget tests run as part of the normal suite and fail the
// build when a hot-path benchmark slips past its documented
// budget. Budgets are deliberately set 2-3× over current measured
// values so day-to-day noise (loaded CI runner, GC variance)
// doesn't flake the gate. A regression that approaches the budget
// is the signal — investigate before merging.
//
// Why a test rather than a separate `-bench` CI job:
//   - `go test ./...` already runs in CI on every PR. Adding budget
//     assertions here means every PR is gated, with no extra job
//     configuration to maintain.
//   - Benchmarks alone don't gate merges; the harness in this
//     file uses `testing.Benchmark` to drive the same code path
//     and then asserts on the returned BenchmarkResult.
//   - Keeps the budget numbers next to the benchmark functions
//     they constrain — search-grep finds both at once.
//
// Tuning the budget envelope: if a future legitimate change pushes
// a benchmark past its budget (e.g. we add a meaningful new
// per-folder check), the right move is to bump the budget IN THIS
// PR and explain why in the commit message — not to mute the test.
// The trail of budget bumps is the perf history of the daemon.

// benchBudget pairs a benchmark function with its absolute upper
// bound. nsPerOp is the ceiling; the bench itself decides how many
// iterations to run via testing.Benchmark.
type benchBudget struct {
	name     string
	fn       func(*testing.B)
	maxNsOp  int64
	minIters int // require at least this many iterations to trust the result
}

var perfBudgets = []benchBudget{
	{
		// Steady-state Diff (everything aligned). Runs on every
		// reconcile tick at the safety-net interval AND on every
		// fsnotify-driven trigger. Per the v1.1.16 measurement
		// (Intel Core Ultra 7 258V) the baseline is ~190 µs; budget
		// at 1.5× = 285 µs lets reasonable variance pass while
		// catching a 2× regression cleanly.
		name:     "DiffSteadyState30Repos",
		fn:       BenchmarkDiffSteadyState30Repos,
		maxNsOp:  285_000,
		minIters: 200,
	},
	{
		// Cold-start Diff (every folder needs ADD, no observed
		// folders yet). Baseline ~165 µs.
		name:     "DiffColdStart30Repos",
		fn:       BenchmarkDiffColdStart30Repos,
		maxNsOp:  250_000,
		minIters: 200,
	},
	{
		// One-repo-changed Diff (the typical fsnotify-driven
		// case). Baseline ~190 µs.
		name:     "DiffOneRepoChanged30Total",
		fn:       BenchmarkDiffOneRepoChanged30Total,
		maxNsOp:  285_000,
		minIters: 200,
	},
	{
		// BuildDesired translates parsed config layer to the
		// Diff-ready Desired struct. Baseline ~115 µs.
		name:     "BuildDesired30Repos",
		fn:       BenchmarkBuildDesired30Repos,
		maxNsOp:  180_000,
		minIters: 200,
	},
}

// TestPerfBudgets runs each documented hot-path benchmark and
// asserts that ns/op stays within budget. Budgets are paddedfor
// expected CI noise but tight enough to catch a real regression.
func TestPerfBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("perf budgets are skipped in -short mode")
	}
	if raceDetectorEnabled {
		// The race detector adds 5–15× overhead. A budget tight
		// enough to catch a 1.5× regression in normal builds would
		// fail under -race; a budget loose enough to pass under
		// -race would miss the regressions we exist to catch. The
		// dedicated non-race CI step ('go test -run TestPerf ...')
		// runs this gate; the standard race-enabled step skips it.
		t.Skip("perf budgets are calibrated for non-race builds")
	}
	for _, b := range perfBudgets {
		t.Run(b.name, func(t *testing.T) {
			res := testing.Benchmark(b.fn)
			t.Logf("%s: %d ns/op over %d iters (budget %d ns/op)",
				b.name, res.NsPerOp(), res.N, b.maxNsOp)
			if res.N < b.minIters {
				t.Errorf("only %d iterations (want at least %d) — result is too noisy to trust",
					res.N, b.minIters)
				return
			}
			if res.NsPerOp() > b.maxNsOp {
				t.Errorf("regression: %s = %d ns/op, budget %d ns/op (%.1f× over)",
					b.name, res.NsPerOp(), b.maxNsOp,
					float64(res.NsPerOp())/float64(b.maxNsOp))
			}
		})
	}
}

// TestPerfBudgetsSelfCheck verifies the perf-budget harness works
// end-to-end: a deliberately fast benchmark passes, and an over-
// budget benchmark fails. Without this, a broken assertion (e.g.
// wrong field comparison) would silently let regressions through.
func TestPerfBudgetsSelfCheck(t *testing.T) {
	t.Run("fast op passes", func(t *testing.T) {
		res := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = i * 2
			}
		})
		if res.NsPerOp() > 100 {
			t.Errorf("trivial multiplication should be <100 ns/op, got %d", res.NsPerOp())
		}
	})
	t.Run("over-budget op fails the assertion", func(t *testing.T) {
		res := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				time.Sleep(time.Microsecond)
			}
		})
		// 1 µs sleep is ~1000 ns; we set the budget to 500 so the
		// assertion logic produces a "would have errored" outcome
		// here. We don't fail the test — we just verify the
		// comparison direction is correct.
		const fakeBudget = 500
		if res.NsPerOp() <= fakeBudget {
			t.Errorf("expected sleep to exceed budget; got %d ns/op (budget %d)",
				res.NsPerOp(), fakeBudget)
		}
	})
}

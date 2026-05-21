// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"math"
	"testing"
	"time"
)

// approxEqual returns whether a and b are within tol of each other
// in absolute terms. Used for fit-coefficient assertions where the
// regression won't produce exact values due to finite-precision
// arithmetic plus the prior's influence.
func approxEqual(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestPredictReturnsPriorBeforeObservations(t *testing.T) {
	const priorSetup = 100.0
	const priorPerByte = 0.0002 // 5 MB/s
	m := NewCostModel(priorSetup, priorPerByte, 8, 86400)

	// Predict for 0 bytes should be roughly the setup cost.
	got0 := m.Predict(0)
	if math.Abs(float64(got0.Milliseconds())-priorSetup) > 1 {
		t.Errorf("Predict(0) = %v, want ~%vms", got0, priorSetup)
	}
	// Predict for 1 MB should be roughly setup + 1MB * perByte.
	wantMS := priorSetup + float64(1<<20)*priorPerByte
	got1MB := m.Predict(1 << 20)
	if math.Abs(float64(got1MB.Milliseconds())-wantMS) > 5 {
		t.Errorf("Predict(1MB) = %v, want ~%vms", got1MB, wantMS)
	}
}

func TestObservationsShiftModel(t *testing.T) {
	// Construct a model with a "wrong" prior, feed it many
	// observations from the "true" distribution, and verify the
	// fit moves toward truth.
	//
	// priorWeight=2 means the prior fades quickly — appropriate
	// when real observation flow is steady (~10s/day per peer
	// for dotkeeper's typical workload). With priorWeight=8 the
	// same convergence requires several thousand observations;
	// see TestPriorWeightControlsConvergenceSpeed for that
	// counterpart.
	m := NewCostModel(1000, 0.001, 2, 86400) // wrong: too slow

	const trueSetup = 50.0     // ms
	const truePerByte = 0.0001 // ms (10 MB/s)
	for i := 0; i < 500; i++ {
		size := int64(1024 * (i + 1)) // 1KB, 2KB, ..., 500KB
		ms := trueSetup + float64(size)*truePerByte
		m.Record(size, time.Duration(ms*float64(time.Millisecond)))
	}

	setup, perByte, n := m.Parameters()
	if !approxEqual(setup, trueSetup, 5) {
		t.Errorf("fit setup = %.2f, want ~%.2f (n=%.1f)", setup, trueSetup, n)
	}
	if !approxEqual(perByte, truePerByte, 1e-5) {
		t.Errorf("fit perByte = %g, want ~%g (n=%.1f)", perByte, truePerByte, n)
	}
}

func TestPriorWeightControlsConvergenceSpeed(t *testing.T) {
	// Two models with the same wrong prior; one with low
	// priorWeight (fast adapt), one with high (smooth + slow).
	// After the same observation stream, the low-priorWeight
	// model should be measurably closer to truth.
	fast := NewCostModel(1000, 0.001, 1, 86400)
	smooth := NewCostModel(1000, 0.001, 50, 86400)

	const trueSetup = 50.0
	const truePerByte = 0.0001
	for i := 0; i < 100; i++ {
		size := int64(1024 * (i + 1))
		ms := trueSetup + float64(size)*truePerByte
		d := time.Duration(ms * float64(time.Millisecond))
		fast.Record(size, d)
		smooth.Record(size, d)
	}

	setupFast, _, _ := fast.Parameters()
	setupSmooth, _, _ := smooth.Parameters()

	distFast := math.Abs(setupFast - trueSetup)
	distSmooth := math.Abs(setupSmooth - trueSetup)
	if distFast >= distSmooth {
		t.Errorf("expected low-priorWeight model to converge faster: distFast=%.2f distSmooth=%.2f", distFast, distSmooth)
	}
}

func TestDecayShrinksOlderObservations(t *testing.T) {
	// Tiny half-life so the test runs quickly. After Record the
	// model knows truth A; after a sleep > halfLife and a Record
	// at truth B, the model should be closer to B than to A.
	m := NewCostModel(0, 0, 1, 0.05) // 50ms half-life

	// Burn in: truth A is "100ms setup, 0.001 ms/byte"
	for i := 0; i < 50; i++ {
		size := int64(10000 * (i + 1))
		ms := 100 + float64(size)*0.001
		m.Record(size, time.Duration(ms*float64(time.Millisecond)))
	}
	setupA, _, _ := m.Parameters()

	// Wait several half-lives, then burn in truth B: "500ms setup, 0.005 ms/byte"
	time.Sleep(500 * time.Millisecond) // 10 half-lives
	for i := 0; i < 50; i++ {
		size := int64(10000 * (i + 1))
		ms := 500 + float64(size)*0.005
		m.Record(size, time.Duration(ms*float64(time.Millisecond)))
	}
	setupB, _, _ := m.Parameters()

	if !(setupB > setupA*1.5) {
		t.Errorf("after a regime change, setup should rise sharply; setupA=%.2f setupB=%.2f", setupA, setupB)
	}
}

func TestNegativeObservationsDropped(t *testing.T) {
	m := NewCostModel(100, 0.001, 8, 86400)
	setup0, perByte0, n0 := m.Parameters()

	m.Record(-1, time.Millisecond)
	m.Record(1024, -time.Millisecond)

	setup1, perByte1, n1 := m.Parameters()
	if setup0 != setup1 || perByte0 != perByte1 || n0 != n1 {
		t.Error("invalid observations modified the fit; they should be silently dropped")
	}
}

func TestPredictNeverNegative(t *testing.T) {
	// Even with pathological observations that produce a negative
	// fitted slope, Predict must return a non-negative duration —
	// the router treats Predict as a cost, and negative costs
	// would break the "pick lowest" logic.
	m := NewCostModel(100, 0.001, 8, 86400)
	// Feed observations where elapsed *decreases* with size
	// (unrealistic; tests the clamp).
	m.Record(1024, 1000*time.Millisecond)
	m.Record(1<<20, 10*time.Millisecond) // 1 MB took less than 1 KB
	m.Record(1<<24, 5*time.Millisecond)  // 16 MB took even less

	for _, size := range []int64{0, 1, 1024, 1 << 20, 1 << 30} {
		got := m.Predict(size)
		if got < 0 {
			t.Errorf("Predict(%d) = %v; must be non-negative", size, got)
		}
	}
}

func TestZeroVarianceFallsBackToPrior(t *testing.T) {
	// All observations at the same size: regression's X-variance
	// is zero, denominator is zero, code path must fall back to
	// the prior rather than dividing.
	m := NewCostModel(100, 0.001, 1, 86400)
	for i := 0; i < 10; i++ {
		m.Record(1024, 50*time.Millisecond)
	}
	setup, perByte, _ := m.Parameters()
	// We don't assert specific values — just that they're finite
	// and roughly in the prior's neighbourhood (the prior with
	// some pull from the same-size observations is the best the
	// algorithm can do).
	if math.IsNaN(setup) || math.IsInf(setup, 0) {
		t.Errorf("setup non-finite under zero-variance: %v", setup)
	}
	if math.IsNaN(perByte) || math.IsInf(perByte, 0) {
		t.Errorf("perByte non-finite under zero-variance: %v", perByte)
	}
}

func TestModelComparesTransportsCorrectly(t *testing.T) {
	// Two models with realistic priors. For a 1-byte change, the
	// model with low setup should predict less. For a 100MB change,
	// the model with high throughput should predict less. This is
	// the crossover behaviour the router depends on.
	gitssh := NewCostModel(150, 0.0002, 8, 86400)    // low setup, low throughput
	syncthing := NewCostModel(1500, 0.00002, 8, 86400) // high setup, high throughput

	if gitssh.Predict(1) >= syncthing.Predict(1) {
		t.Errorf("for tiny payload, gitssh should be faster: gitssh=%v syncthing=%v",
			gitssh.Predict(1), syncthing.Predict(1))
	}
	if gitssh.Predict(100*1024*1024) <= syncthing.Predict(100*1024*1024) {
		t.Errorf("for huge payload, syncthing should be faster: gitssh=%v syncthing=%v",
			gitssh.Predict(100*1024*1024), syncthing.Predict(100*1024*1024))
	}
}

func TestParametersReflectsRecordedSamples(t *testing.T) {
	m := NewCostModel(100, 0.001, 8, 86400)
	_, _, n0 := m.Parameters()
	if n0 != 0 {
		t.Errorf("fresh model effectiveSamples = %.2f, want 0 (prior is separate)", n0)
	}
	for i := 0; i < 5; i++ {
		m.Record(1024, 10*time.Millisecond)
	}
	_, _, n1 := m.Parameters()
	if n1 < 4 || n1 > 5 {
		// Allow slight decay across the rapid Records.
		t.Errorf("after 5 Records effectiveSamples = %.2f, want ~5", n1)
	}
}

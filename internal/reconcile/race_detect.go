// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !race

package reconcile

// raceDetectorEnabled reports whether this build was compiled with
// the -race flag. The perf-budget gate uses this to skip itself
// under -race, because the race detector adds 5–15× overhead and
// any meaningful CPU budget would have to be padded to the point
// of uselessness. The non-race CI step still runs the gate.
const raceDetectorEnabled = false

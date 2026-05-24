// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build race

package reconcile

// See race_detect.go for the rationale. The race-enabled variant
// of this constant is what the perf-budget test reads to skip
// itself under -race.
const raceDetectorEnabled = true

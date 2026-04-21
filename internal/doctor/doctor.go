// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package doctor runs a sequence of small self-checks that answer the
// question "is dotkeeper healthy on this machine?". Each check is
// self-contained and reports a single one-line outcome plus an optional
// remediation hint. The orchestrator prints them in order and returns
// the number of actively failed checks so the CLI layer can set an
// appropriate exit code.
//
// Checks must never panic and must handle their own dependency errors —
// a check that cannot run (e.g. because Syncthing isn't responding) can
// legitimately report Fail, but it must not abort the rest of the run.
package doctor

import (
	"context"
	"io"
)

// Outcome is the verdict of a single Check.
type Outcome int

const (
	// OK means the check passed. Nothing to investigate.
	OK Outcome = iota
	// Warn means the state is unusual but not actionable as a failure
	// (e.g. a peer offline, a transient syncing folder).
	Warn
	// Fail means the check actively failed and dotkeeper is likely not
	// functioning correctly on this machine.
	Fail
)

// String returns a short lowercase label — useful for JSON output and
// for test assertions. Keep in sync with the Outcome constants.
func (o Outcome) String() string {
	switch o {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	}
	return "unknown"
}

// Result is what a Check returns. Detail is a one-line human-readable
// summary; Hint is an optional one-line remediation suggestion shown on
// a second indented line when the outcome is not OK.
type Result struct {
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail"`
	Hint    string  `json:"hint,omitempty"`
}

// Check is a single diagnostic. Name must be stable and short — it is
// used as the left-hand label in the pretty output. Run must not panic
// and should honour ctx for any outbound I/O with a short timeout.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// Run executes every check in order, writes the formatted output to w,
// and returns the count of Fail outcomes. Warnings do not contribute to
// the failure count.
//
// The caller owns the writer; Run does not close it.
func Run(ctx context.Context, checks []Check, w io.Writer) int {
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		r := c.Run(ctx)
		if r.Name == "" {
			// Defensive: a misbehaving check with an empty name would
			// destroy the column alignment of the pretty output.
			r.Name = c.Name()
		}
		results = append(results, r)
	}
	WriteText(w, results)
	return countFailures(results)
}

// RunJSON is the JSON-output equivalent of Run. It emits a single JSON
// object with a results array so a consumer can machine-parse the full
// diagnostic in one read.
func RunJSON(ctx context.Context, checks []Check, w io.Writer) int {
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		r := c.Run(ctx)
		if r.Name == "" {
			r.Name = c.Name()
		}
		results = append(results, r)
	}
	WriteJSON(w, results)
	return countFailures(results)
}

// countFailures counts the number of Fail outcomes in a result set.
// Exposed via the return value of Run and RunJSON for the CLI exit code.
func countFailures(results []Result) int {
	var n int
	for _, r := range results {
		if r.Outcome == Fail {
			n++
		}
	}
	return n
}

// countWarnings counts the number of Warn outcomes — used in the footer
// of the pretty output.
func countWarnings(results []Result) int {
	var n int
	for _, r := range results {
		if r.Outcome == Warn {
			n++
		}
	}
	return n
}

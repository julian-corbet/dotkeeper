// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeCheck is a programmable Check whose Name/Run results are set by the test.
type fakeCheck struct {
	name   string
	result Result
}

func (f *fakeCheck) Name() string                 { return f.name }
func (f *fakeCheck) Run(_ context.Context) Result { return f.result }

func TestOutcomeString(t *testing.T) {
	cases := []struct {
		o    Outcome
		want string
	}{
		{OK, "ok"},
		{Warn, "warn"},
		{Fail, "fail"},
		{Outcome(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.o.String(); got != c.want {
			t.Errorf("Outcome(%d).String() = %q, want %q", c.o, got, c.want)
		}
	}
}

func TestRunCountsFailuresNotWarnings(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "a", result: Result{Name: "a", Outcome: OK, Detail: "ok"}},
		&fakeCheck{name: "b", result: Result{Name: "b", Outcome: Warn, Detail: "warn"}},
		&fakeCheck{name: "c", result: Result{Name: "c", Outcome: Fail, Detail: "bad", Hint: "try X"}},
		&fakeCheck{name: "d", result: Result{Name: "d", Outcome: Fail, Detail: "worse"}},
	}
	var buf bytes.Buffer
	fails := Run(context.Background(), checks, &buf)
	if fails != 2 {
		t.Errorf("Run returned %d failures, want 2", fails)
	}
	out := buf.String()
	if !strings.Contains(out, "Found 2 issues") {
		t.Errorf("missing footer; got:\n%s", out)
	}
	if !strings.Contains(out, "try X") {
		t.Errorf("hint missing from output; got:\n%s", out)
	}
}

func TestRunAllOK(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "a", result: Result{Name: "a", Outcome: OK, Detail: "ok"}},
		&fakeCheck{name: "b", result: Result{Name: "b", Outcome: OK, Detail: "ok"}},
	}
	var buf bytes.Buffer
	fails := Run(context.Background(), checks, &buf)
	if fails != 0 {
		t.Errorf("Run returned %d failures, want 0", fails)
	}
	if !strings.Contains(buf.String(), "Everything looks healthy.") {
		t.Errorf("missing healthy footer; got:\n%s", buf.String())
	}
}

func TestRunAllWarnings(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "a", result: Result{Name: "a", Outcome: OK, Detail: "ok"}},
		&fakeCheck{name: "b", result: Result{Name: "b", Outcome: Warn, Detail: "warn"}},
	}
	var buf bytes.Buffer
	fails := Run(context.Background(), checks, &buf)
	if fails != 0 {
		t.Errorf("Run returned %d failures, want 0", fails)
	}
	out := buf.String()
	if !strings.Contains(out, "Found 0 issues, 1 warning.") {
		t.Errorf("expected singular warning footer; got:\n%s", out)
	}
}

func TestRunFillsEmptyNameFromCheckName(t *testing.T) {
	// A Check that returns a Result without setting Name should still
	// render with the check's declared Name.
	c := &fakeCheck{name: "declared-name", result: Result{Outcome: OK, Detail: "ok"}}
	var buf bytes.Buffer
	_ = Run(context.Background(), []Check{c}, &buf)
	if !strings.Contains(buf.String(), "declared-name") {
		t.Errorf("expected check Name to backfill Result.Name; got:\n%s", buf.String())
	}
}

func TestRunJSONEmitsStableShape(t *testing.T) {
	checks := []Check{
		&fakeCheck{name: "a", result: Result{Name: "a", Outcome: OK, Detail: "ok"}},
		&fakeCheck{name: "b", result: Result{Name: "b", Outcome: Fail, Detail: "boom", Hint: "fix it"}},
	}
	var buf bytes.Buffer
	fails := RunJSON(context.Background(), checks, &buf)
	if fails != 1 {
		t.Errorf("RunJSON returned %d failures, want 1", fails)
	}
	var payload struct {
		Results []struct {
			Name    string `json:"name"`
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
			Hint    string `json:"hint,omitempty"`
		} `json:"results"`
		Failures int `json:"failures"`
		Warnings int `json:"warnings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("JSON decode: %v\n%s", err, buf.String())
	}
	if payload.Failures != 1 {
		t.Errorf("failures = %d, want 1", payload.Failures)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(payload.Results))
	}
	if payload.Results[1].Outcome != "fail" {
		t.Errorf("Outcome = %q, want fail", payload.Results[1].Outcome)
	}
	if payload.Results[1].Hint != "fix it" {
		t.Errorf("Hint = %q, want 'fix it'", payload.Results[1].Hint)
	}
}

func TestFormatOptionsASCII(t *testing.T) {
	results := []Result{
		{Name: "a", Outcome: OK, Detail: "ok"},
		{Name: "b", Outcome: Warn, Detail: "warn"},
		{Name: "c", Outcome: Fail, Detail: "bad", Hint: "try X"},
	}
	var buf bytes.Buffer
	WriteTextWithOptions(&buf, results, FormatOptions{Color: false, ASCII: true})
	out := buf.String()
	for _, want := range []string{"[ok]", "[warn]", "[fail]", "-> try X"} {
		if !strings.Contains(out, want) {
			t.Errorf("ASCII output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[") {
		t.Errorf("ASCII output must not contain ANSI escapes; got:\n%s", out)
	}
}

func TestFormatOptionsColor(t *testing.T) {
	results := []Result{
		{Name: "a", Outcome: OK, Detail: "ok"},
	}
	var buf bytes.Buffer
	WriteTextWithOptions(&buf, results, FormatOptions{Color: true, ASCII: false})
	out := buf.String()
	if !strings.Contains(out, "\033[32m") {
		t.Errorf("expected green ANSI code in colour output; got:\n%s", out)
	}
}

func TestFormatHintOnlyShownForNonOK(t *testing.T) {
	results := []Result{
		{Name: "a", Outcome: OK, Detail: "ok", Hint: "should not appear"},
		{Name: "b", Outcome: Fail, Detail: "bad", Hint: "should appear"},
	}
	var buf bytes.Buffer
	WriteTextWithOptions(&buf, results, FormatOptions{})
	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("hint leaked into OK result; got:\n%s", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("hint missing from failing result; got:\n%s", out)
	}
}

func TestFormatPluralGrammar(t *testing.T) {
	// 1 issue / 1 warning — both singular
	var buf bytes.Buffer
	WriteTextWithOptions(&buf, []Result{
		{Name: "a", Outcome: Fail, Detail: "x"},
		{Name: "b", Outcome: Warn, Detail: "y"},
	}, FormatOptions{})
	if !strings.Contains(buf.String(), "Found 1 issue, 1 warning.") {
		t.Errorf("singular grammar wrong; got:\n%s", buf.String())
	}

	// 0 issues / 2 warnings
	buf.Reset()
	WriteTextWithOptions(&buf, []Result{
		{Name: "a", Outcome: Warn, Detail: "x"},
		{Name: "b", Outcome: Warn, Detail: "y"},
	}, FormatOptions{})
	if !strings.Contains(buf.String(), "Found 0 issues, 2 warnings.") {
		t.Errorf("plural grammar wrong; got:\n%s", buf.String())
	}
}

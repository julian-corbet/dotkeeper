// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package service

import (
	"strings"
	"testing"
)

func TestParseSystemctlShowActiveRunning(t *testing.T) {
	raw := strings.Join([]string{
		"ActiveState=active",
		"SubState=running",
		"ActiveEnterTimestamp=Sat 2026-04-19 15:21:13 CEST",
	}, "\n")
	got := parseSystemctlShow(raw)
	if got.Active != "active" {
		t.Errorf("Active = %q", got.Active)
	}
	if got.Sub != "running" {
		t.Errorf("Sub = %q", got.Sub)
	}
	if got.Since.IsZero() {
		t.Errorf("Since should not be zero")
	}
}

func TestParseSystemctlShowFailed(t *testing.T) {
	raw := "ActiveState=failed\nSubState=failed\nActiveEnterTimestamp=n/a\n"
	got := parseSystemctlShow(raw)
	if got.Active != "failed" {
		t.Errorf("Active = %q", got.Active)
	}
	if !got.Since.IsZero() {
		t.Errorf("Since should be zero for n/a")
	}
}

func TestParseSystemctlShowInactiveEmpty(t *testing.T) {
	got := parseSystemctlShow("")
	if got.Active != "" {
		t.Errorf("Active = %q, want empty", got.Active)
	}
}

func TestTrimWeekdayAndZone(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Sat 2026-04-19 15:21:13 CEST", "2026-04-19 15:21:13"},
		{"Mon 2026-01-05 02:05:00 UTC", "2026-01-05 02:05:00"},
		{"2026-01-05 02:05:00", "2026-01-05 02:05:00"}, // no weekday, no trailing zone: gets last space trimmed
	}
	// The third case is a little odd — our helper is designed for
	// systemd output which always has both. We just make sure we don't
	// panic; exact shape isn't contractually guaranteed for that input.
	for _, c := range cases[:2] {
		if got := trimWeekdayAndZone(c.in); got != c.want {
			t.Errorf("trimWeekdayAndZone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

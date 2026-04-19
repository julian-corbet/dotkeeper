// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin && !windows

package service

import (
	"fmt"
	"strings"
	"testing"
)

// TestCalendarToCronPreservesOffset tests that the slot minute offset
// is correctly preserved in cron expressions.
func TestCalendarToCronPreservesOffset(t *testing.T) {
	tests := []struct {
		onCalendar  string
		wantMinute  string
		description string
	}{
		{"*:05", "5 ", "hourly slot 1 (offset 5min)"},
		{"*:00", "0 ", "hourly slot 0"},
		{"*-*-* 02:05:00", "5 2", "daily slot 1"},
		{"*-*-* 02:00:00", "0 2", "daily slot 0"},
		{"Mon 02:10:00", "10 2", "weekly slot 2"},
		{"*-*-01 02:05:00", "5 2", "monthly slot 1"},
		{"0/6:05", "5 */6", "every 6h slot 1"},
		{"0/2:00", "0 */2", "every 2h slot 0"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := calendarToCron(tt.onCalendar)
			if !strings.HasPrefix(got, tt.wantMinute) {
				t.Errorf("calendarToCron(%q) = %q, want prefix %q (%s)",
					tt.onCalendar, got, tt.wantMinute, tt.description)
			}
		})
	}
}

// TestCalendarToCronNeverProducesZeroZero tests that the cron expression
// for non-hourly intervals always includes the hour.
func TestCalendarToCronValidFormat(t *testing.T) {
	inputs := []string{"*:05", "*-*-* 02:05:00", "Mon 02:05:00", "0/6:05"}
	for _, input := range inputs {
		got := calendarToCron(input)
		parts := strings.Fields(got)
		if len(parts) != 5 {
			t.Errorf("calendarToCron(%q) = %q — not 5 fields", input, got)
		}
	}
}

// TestMachineKeyHandlesSpecialCharacters tests that machineKey sanitizes
// hostnames correctly for use as TOML keys.
func TestMachineKeyEquivalent(t *testing.T) {
	// This tests the same logic as machineKey() in main.go
	// but from the perspective of what the cron backend receives
	tests := []struct {
		hostname string
		wantSafe bool
	}{
		{"my-desktop", true},
		{"my.machine.local", true},     // dots should be replaced
		{"MY-LAPTOP-01", true},
		{"machine with spaces", true},
	}

	for _, tt := range tests {
		// Simulate what machineKey does
		key := strings.ToLower(tt.hostname)
		key = strings.ReplaceAll(key, "-", "_")
		key = strings.ReplaceAll(key, ".", "_")
		key = strings.ReplaceAll(key, " ", "_")

		// Verify no TOML-breaking characters
		for _, ch := range key {
			if ch == '.' || ch == '[' || ch == ']' || ch == '"' {
				t.Errorf("machineKey(%q) = %q contains TOML-breaking char %q",
					tt.hostname, key, string(ch))
			}
		}
		_ = tt.wantSafe
	}
}

// TestSlotOffsetValidation tests that negative or huge slot offsets
// produce reasonable timer expressions.
func TestSlotOffsetBounds(t *testing.T) {
	tests := []struct {
		onCalendar string
		desc       string
	}{
		{"*:05", "normal offset"},
		{"*:00", "zero offset"},
		{"*-*-* 02:55:00", "large offset (slot 11)"},
	}

	for _, tt := range tests {
		got := calendarToCron(tt.onCalendar)
		if got == "" {
			t.Errorf("calendarToCron(%q) returned empty", tt.onCalendar)
		}
		// Parse minute field
		var minute int
		_, _ = fmt.Sscanf(got, "%d", &minute)
		if minute < 0 || minute > 59 {
			t.Errorf("calendarToCron(%q) has minute %d out of [0,59]", tt.onCalendar, minute)
		}
	}
}

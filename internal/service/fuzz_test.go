// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin && !windows

package service

import (
	"strings"
	"testing"
)

// FuzzCalendarToCron tests that calendarToCron never panics on arbitrary input
// and always produces a 5-field cron expression.
func FuzzCalendarToCron(f *testing.F) {
	// Seed with real-world inputs
	f.Add("*:05")
	f.Add("*:00")
	f.Add("*-*-* 02:05:00")
	f.Add("*-*-* 02:00:00")
	f.Add("Mon 02:10:00")
	f.Add("*-*-01 02:05:00")
	f.Add("0/6:05")
	f.Add("0/2:00")
	f.Add("0/12:30")
	f.Add("")
	f.Add(":")
	f.Add("garbage")
	f.Add("Mon 99:99:99")
	f.Add("0/0:00")
	f.Add(strings.Repeat("A", 10000))

	f.Fuzz(func(t *testing.T, input string) {
		result := calendarToCron(input)

		// Must never return empty
		if result == "" {
			t.Errorf("calendarToCron(%q) returned empty string", input)
		}

		// Must always produce exactly 5 fields
		fields := strings.Fields(result)
		if len(fields) != 5 {
			t.Errorf("calendarToCron(%q) = %q — got %d fields, want 5", input, result, len(fields))
		}
	})
}

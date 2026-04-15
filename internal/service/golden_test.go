// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin && !windows

package service

import (
	"fmt"
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/testutil"
)

// Golden file tests snapshot the exact output of service unit generation.
// Any accidental format change breaks the golden file.
//
// To update golden files after intentional changes:
//   go test -update ./internal/service/
//
// To add a new golden test:
//   1. Write a TestGolden* function below
//   2. Run: go test -update -run TestGoldenNewTest ./internal/service/
//   3. Review the generated .golden file
//   4. Commit both the test and the golden file

// --- Cron expression golden tests ---

func TestGoldenCronHourly(t *testing.T) {
	got := calendarToCron("*:05")
	testutil.GoldenCheck(t, "cron_hourly", got)
}

func TestGoldenCronDaily(t *testing.T) {
	got := calendarToCron("*-*-* 02:05:00")
	testutil.GoldenCheck(t, "cron_daily", got)
}

func TestGoldenCronWeekly(t *testing.T) {
	got := calendarToCron("Mon 02:10:00")
	testutil.GoldenCheck(t, "cron_weekly", got)
}

func TestGoldenCronMonthly(t *testing.T) {
	got := calendarToCron("*-*-01 02:05:00")
	testutil.GoldenCheck(t, "cron_monthly", got)
}

func TestGoldenCronEvery6h(t *testing.T) {
	got := calendarToCron("0/6:05")
	testutil.GoldenCheck(t, "cron_every6h", got)
}

func TestGoldenCronEvery2h(t *testing.T) {
	got := calendarToCron("0/2:00")
	testutil.GoldenCheck(t, "cron_every2h", got)
}

// --- Systemd unit golden tests ---
// These test the string format without actually calling systemctl.

func TestGoldenSystemdSyncthingUnit(t *testing.T) {
	got := fmt.Sprintf(`[Unit]
Description=dotkeeper embedded Syncthing instance
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="%s" start
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, "/usr/local/bin/dotkeeper")
	testutil.GoldenCheck(t, "systemd_syncthing_unit", got)
}

func TestGoldenSystemdTimerUnit(t *testing.T) {
	got := fmt.Sprintf(`[Unit]
Description=dotkeeper git auto-backup timer

[Timer]
OnCalendar=%s
Persistent=true
RandomizedDelaySec=30

[Install]
WantedBy=timers.target
`, "*-*-* 02:05:00")
	testutil.GoldenCheck(t, "systemd_timer_unit", got)
}

func TestGoldenSystemdSyncService(t *testing.T) {
	got := fmt.Sprintf(`[Unit]
Description=dotkeeper git auto-backup
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart="%s" sync
`, "/usr/local/bin/dotkeeper")
	testutil.GoldenCheck(t, "systemd_sync_service", got)
}

// --- calendarToCron comprehensive table ---
// Ensures all schedule types produce expected cron expressions.

func TestCalendarToCronAllSchedules(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Hourly
		{"*:00", "0 * * * *"},
		{"*:05", "5 * * * *"},
		{"*:30", "30 * * * *"},
		{"*:59", "59 * * * *"},
		// Daily
		{"*-*-* 02:05:00", "5 2 * * *"},
		{"*-*-* 00:00:00", "0 0 * * *"},
		{"*-*-* 23:59:00", "59 23 * * *"},
		// Weekly
		{"Mon 02:05:00", "5 2 * * 1"},
		{"Mon 00:00:00", "0 0 * * 1"},
		// Monthly
		{"*-*-01 02:05:00", "5 2 1 * *"},
		// Every N hours
		{"0/2:00", "0 */2 * * *"},
		{"0/2:15", "15 */2 * * *"},
		{"0/3:05", "5 */3 * * *"},
		{"0/4:00", "0 */4 * * *"},
		{"0/6:05", "5 */6 * * *"},
		{"0/8:00", "0 */8 * * *"},
		{"0/12:30", "30 */12 * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := calendarToCron(tt.input)
			if got != tt.want {
				t.Errorf("calendarToCron(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

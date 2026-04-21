// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Systemd implements Manager for Linux systems with systemd.
type Systemd struct{}

func (s *Systemd) Name() string { return "systemd" }

func isSystemd() bool {
	// systemd sets this at boot
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

func (s *Systemd) unitDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func (s *Systemd) writeUnit(filename, content string) error {
	dir := s.unitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, filename)
	return os.WriteFile(path, []byte(content), 0o644)
}

func (s *Systemd) InstallSyncthing(binaryPath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=dotkeeper embedded Syncthing instance
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="%s" start
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, binaryPath)

	if err := s.writeUnit("dotkeeper-syncthing.service", unit); err != nil {
		return err
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return exec.Command("systemctl", "--user", "enable", "--now", "dotkeeper-syncthing.service").Run()
}

func (s *Systemd) InstallTimer(binaryPath, configPath, onCalendar string) error {
	svc := fmt.Sprintf(`[Unit]
Description=dotkeeper git auto-backup
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart="%s" sync
`, binaryPath)

	tmr := fmt.Sprintf(`[Unit]
Description=dotkeeper git auto-backup timer

[Timer]
OnCalendar=%s
Persistent=true
RandomizedDelaySec=30

[Install]
WantedBy=timers.target
`, onCalendar)

	if err := s.writeUnit("dotkeeper-sync.service", svc); err != nil {
		return err
	}
	if err := s.writeUnit("dotkeeper-sync.timer", tmr); err != nil {
		return err
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return exec.Command("systemctl", "--user", "enable", "--now", "dotkeeper-sync.timer").Run()
}

func (s *Systemd) StartSyncthing() error {
	return exec.Command("systemctl", "--user", "start", "dotkeeper-syncthing.service").Run()
}

func (s *Systemd) StopSyncthing() error {
	return exec.Command("systemctl", "--user", "stop", "dotkeeper-syncthing.service").Run()
}

func (s *Systemd) IsSyncthingRunning() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", "dotkeeper-syncthing.service").Run() == nil
}

func (s *Systemd) IsTimerActive() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", "dotkeeper-sync.timer").Run() == nil
}

// SyncthingStatus returns the ActiveState/SubState/since triple for the
// dotkeeper-syncthing.service user unit. Missing fields come back empty
// so callers can detect "never installed" (Active=="") vs "stopped"
// (Active=="inactive") without heuristics.
func (s *Systemd) SyncthingStatus() SyncthingUnitStatus {
	out, err := exec.Command("systemctl", "--user", "show",
		"dotkeeper-syncthing.service",
		"--property=ActiveState,SubState,ActiveEnterTimestamp",
	).Output()
	if err != nil {
		return SyncthingUnitStatus{}
	}
	return parseSystemctlShow(string(out))
}

// TimerNext returns the scheduled next-fire time for the
// dotkeeper-sync.timer user unit. Zero-valued if the timer is inactive
// or systemd doesn't populate the property yet (can happen in the few
// seconds after `enable --now`).
func (s *Systemd) TimerNext() TimerNextRun {
	out, err := exec.Command("systemctl", "--user", "show",
		"dotkeeper-sync.timer",
		"--property=NextElapseUSecRealtime",
	).Output()
	if err != nil {
		return TimerNextRun{}
	}
	return parseTimerNext(string(out))
}

// parseSystemctlShow parses the key=value output of `systemctl show`.
// Exposed for unit testing — the logic is too easy to subtly break
// when the only validation path is end-to-end on a live systemd.
func parseSystemctlShow(raw string) SyncthingUnitStatus {
	var st SyncthingUnitStatus
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.Active = v
		case "SubState":
			st.Sub = v
		case "ActiveEnterTimestamp":
			// systemd format: "Sat 2026-04-19 15:21:13 CEST" (or "n/a").
			if v == "" || v == "n/a" {
				continue
			}
			// Try a couple of common layouts. Systemd's timestamp always
			// contains a weekday + timezone abbreviation which Go cannot
			// parse natively, so strip them first.
			trimmed := trimWeekdayAndZone(v)
			// "2006-01-02 15:04:05" — timezone-naive local time.
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, time.Local); err == nil {
				st.Since = t
			}
		}
	}
	return st
}

// parseTimerNext extracts NextElapseUSecRealtime from a `systemctl show`
// block. The value is microseconds-since-epoch or "0" when inactive.
func parseTimerNext(raw string) TimerNextRun {
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || k != "NextElapseUSecRealtime" {
			continue
		}
		if v == "" || v == "0" {
			return TimerNextRun{}
		}
		us, err := strconv.ParseInt(v, 10, 64)
		if err != nil || us <= 0 {
			return TimerNextRun{}
		}
		t := time.Unix(0, us*1000)
		return TimerNextRun{
			Next: t,
			Raw:  t.Local().Format("Mon 2006-01-02 15:04 MST"),
		}
	}
	return TimerNextRun{}
}

// trimWeekdayAndZone strips a leading weekday abbreviation (e.g. "Sat ")
// and a trailing timezone token (e.g. " CEST") from a systemd timestamp
// so the middle chunk is parseable by Go's time package in Local.
func trimWeekdayAndZone(ts string) string {
	ts = strings.TrimSpace(ts)
	// Leading "Mon " / "Tue " etc.
	parts := strings.SplitN(ts, " ", 2)
	if len(parts) == 2 && len(parts[0]) == 3 {
		ts = parts[1]
	}
	// Trailing timezone token.
	if idx := strings.LastIndex(ts, " "); idx >= 0 {
		ts = ts[:idx]
	}
	return ts
}

func (s *Systemd) DaemonReload() error {
	return exec.Command("systemctl", "--user", "daemon-reload").Run()
}

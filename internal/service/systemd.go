// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func (s *Systemd) DaemonReload() error {
	return exec.Command("systemctl", "--user", "daemon-reload").Run()
}

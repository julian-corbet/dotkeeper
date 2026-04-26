// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin && !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var pidRegex = regexp.MustCompile(`^\d+$`)

// Cron implements Manager for BSDs and other Unix systems using cron.
// Syncthing is managed via a simple PID file and background process.
type Cron struct{}

func (c *Cron) Name() string { return "cron" }

func (c *Cron) pidFile() string {
	home, _ := os.UserHomeDir()
	return home + "/.local/share/dotkeeper/syncthing.pid"
}

func (c *Cron) InstallSyncthing(binaryPath string) error {
	// Check if already running
	if c.IsSyncthingRunning() {
		return nil
	}

	// Start in background with nohup, detached from terminal
	cmd := exec.Command("nohup", binaryPath, "start")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting syncthing: %w", err)
	}
	pid := cmd.Process.Pid
	if err := os.MkdirAll(filepath.Dir(c.pidFile()), 0o700); err != nil {
		return fmt.Errorf("creating PID directory: %w", err)
	}
	if err := os.WriteFile(c.pidFile(), []byte(fmt.Sprintf("%d", pid)), 0o600); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}

	// Release the process so it survives after we exit
	_ = cmd.Process.Release()

	// Add to crontab for reboot persistence
	return c.addCrontab(fmt.Sprintf("@reboot %s start", binaryPath))
}

func (c *Cron) InstallTimer(binaryPath, configPath, onCalendar string) error {
	cronExpr := calendarToCron(onCalendar)
	return c.addCrontab(fmt.Sprintf("%s %s sync", cronExpr, binaryPath))
}

func (c *Cron) StartSyncthing() error {
	// Already handled by InstallSyncthing or cron @reboot
	return nil
}

func (c *Cron) StopSyncthing() error {
	data, err := os.ReadFile(c.pidFile())
	if err != nil {
		return nil // not running
	}
	pid := strings.TrimSpace(string(data))
	if !pidRegex.MatchString(pid) {
		_ = os.Remove(c.pidFile())
		return fmt.Errorf("corrupt PID file: %q", pid)
	}
	// Best-effort stop: a kill failure (already dead, no permission) is not
	// actionable here and shouldn't prevent us from cleaning up the PID file.
	_ = exec.Command("kill", pid).Run()
	_ = os.Remove(c.pidFile())
	return nil
}

func (c *Cron) IsSyncthingRunning() bool {
	data, err := os.ReadFile(c.pidFile())
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	if !pidRegex.MatchString(pid) {
		return false
	}
	return exec.Command("kill", "-0", pid).Run() == nil
}

func (c *Cron) IsTimerActive() bool {
	out, _ := exec.Command("crontab", "-l").Output()
	// Check for both v0.4 ("dotkeeper sync") and v0.5 ("dotkeeper reconcile")
	// cron entries so detection works across the migration boundary.
	s := string(out)
	return containsStr(s, "dotkeeper sync") || containsStr(s, "dotkeeper reconcile")
}

func (c *Cron) DaemonReload() error {
	return nil
}

func (c *Cron) addCrontab(line string) error {
	out, _ := exec.Command("crontab", "-l").Output()
	existing := string(out)

	// Don't add duplicates
	if containsStr(existing, line) {
		return nil
	}

	newCrontab := strings.TrimRight(existing, "\n") + "\n" + line + "\n"
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(newCrontab)
	return cmd.Run()
}

func calendarToCron(onCalendar string) string {
	// Extract hour and minute from systemd OnCalendar expressions.
	// Patterns: "*:05", "*-*-* 02:05:00", "Mon 02:05:00", "0/6:05"
	hour, minute := 2, 0
	for i := 0; i < len(onCalendar)-1; i++ {
		if onCalendar[i] == ':' && i > 0 {
			// Parse failures leave the zero-valued default in place, which is fine.
			_, _ = fmt.Sscanf(onCalendar[i+1:], "%d", &minute)
			before := onCalendar[:i]
			lastSpace := strings.LastIndex(before, " ")
			hourStr := before
			if lastSpace >= 0 {
				hourStr = before[lastSpace+1:]
			}
			_, _ = fmt.Sscanf(hourStr, "%d", &hour)
			break
		}
	}

	switch {
	case containsStr(onCalendar, "Mon"):
		return fmt.Sprintf("%d %d * * 1", minute, hour) // weekly
	case containsStr(onCalendar, "*-*-01"):
		return fmt.Sprintf("%d %d 1 * *", minute, hour) // monthly
	case containsStr(onCalendar, "0/2:"):
		return fmt.Sprintf("%d */%d * * *", minute, 2) // every 2h
	case containsStr(onCalendar, "0/3:"):
		return fmt.Sprintf("%d */%d * * *", minute, 3)
	case containsStr(onCalendar, "0/4:"):
		return fmt.Sprintf("%d */%d * * *", minute, 4)
	case containsStr(onCalendar, "0/6:"):
		return fmt.Sprintf("%d */%d * * *", minute, 6)
	case containsStr(onCalendar, "0/8:"):
		return fmt.Sprintf("%d */%d * * *", minute, 8)
	case containsStr(onCalendar, "0/12:"):
		return fmt.Sprintf("%d */%d * * *", minute, 12)
	case containsStr(onCalendar, "*-*-*"):
		return fmt.Sprintf("%d %d * * *", minute, hour) // daily
	default:
		return fmt.Sprintf("%d * * * *", minute) // hourly
	}
}

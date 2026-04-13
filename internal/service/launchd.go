// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Launchd implements Manager for macOS.
type Launchd struct{}

func (l *Launchd) Name() string { return "launchd" }

const (
	syncthingLabel = "ch.corbet.dotkeeper.syncthing"
	timerLabel     = "ch.corbet.dotkeeper.sync"
)

func (l *Launchd) agentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents")
}

func (l *Launchd) writePlist(label, content string) error {
	dir := l.agentDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, label+".plist")
	return os.WriteFile(path, []byte(content), 0o644)
}

func (l *Launchd) InstallSyncthing(binaryPath string) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/dotkeeper-syncthing.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/dotkeeper-syncthing.log</string>
</dict>
</plist>
`, syncthingLabel, binaryPath)

	if err := l.writePlist(syncthingLabel, plist); err != nil {
		return err
	}
	// bootstrap is the modern replacement for the deprecated 'load'
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	plistPath := filepath.Join(l.agentDir(), syncthingLabel+".plist")
	if err := exec.Command("launchctl", "bootstrap", uid, plistPath).Run(); err != nil {
		// Fall back to deprecated 'load' for older macOS
		return exec.Command("launchctl", "load", plistPath).Run()
	}
	return nil
}

func (l *Launchd) InstallTimer(binaryPath, configPath, onCalendar string) error {
	schedule := launchdSchedule(onCalendar)

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>sync</string>
    </array>
%s
    <key>StandardOutPath</key>
    <string>/tmp/dotkeeper-sync.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/dotkeeper-sync.log</string>
</dict>
</plist>
`, timerLabel, binaryPath, schedule)

	if err := l.writePlist(timerLabel, plist); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	plistPath := filepath.Join(l.agentDir(), timerLabel+".plist")
	if err := exec.Command("launchctl", "bootstrap", uid, plistPath).Run(); err != nil {
		return exec.Command("launchctl", "load", plistPath).Run()
	}
	return nil
}

func (l *Launchd) StartSyncthing() error {
	return exec.Command("launchctl", "start", syncthingLabel).Run()
}

func (l *Launchd) StopSyncthing() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	if err := exec.Command("launchctl", "kill", "SIGTERM", uid+"/"+syncthingLabel).Run(); err != nil {
		return exec.Command("launchctl", "stop", syncthingLabel).Run()
	}
	return nil
}

func (l *Launchd) IsSyncthingRunning() bool {
	out, err := exec.Command("launchctl", "list", syncthingLabel).Output()
	return err == nil && len(out) > 0
}

func (l *Launchd) IsTimerActive() bool {
	out, err := exec.Command("launchctl", "list", timerLabel).Output()
	return err == nil && len(out) > 0
}

func (l *Launchd) DaemonReload() error {
	return nil // launchd doesn't need this
}

// launchdSchedule generates the plist XML fragment for scheduling.
// Handles hourly (Minute only), every-N-hours (StartInterval), and
// daily/weekly/monthly (Hour + Minute via StartCalendarInterval).
func launchdSchedule(onCalendar string) string {
	// Extract minute from the onCalendar expression
	var hour, minute int
	hourFound := false
	for i := 0; i < len(onCalendar)-1; i++ {
		if onCalendar[i] == ':' && i > 0 {
			fmt.Sscanf(onCalendar[i+1:], "%d", &minute)
			before := onCalendar[:i]
			lastSpace := strings.LastIndex(before, " ")
			hourStr := before
			if lastSpace >= 0 {
				hourStr = before[lastSpace+1:]
			}
			if _, err := fmt.Sscanf(hourStr, "%d", &hour); err == nil {
				hourFound = true
			}
			break
		}
	}

	// Detect every-N-hours patterns (0/2:, 0/6:, etc.) — use StartInterval
	for _, n := range []int{2, 3, 4, 6, 8, 12} {
		if containsStr(onCalendar, fmt.Sprintf("0/%d:", n)) {
			seconds := n * 3600
			return fmt.Sprintf("    <key>StartInterval</key>\n    <integer>%d</integer>", seconds)
		}
	}

	// Hourly: only Minute key (no Hour = runs every hour)
	if !hourFound {
		return fmt.Sprintf(`    <key>StartCalendarInterval</key>
    <dict>
        <key>Minute</key>
        <integer>%d</integer>
    </dict>`, minute)
	}

	// Daily/weekly/monthly: both Hour and Minute
	s := fmt.Sprintf(`    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key>
        <integer>%d</integer>
        <key>Minute</key>
        <integer>%d</integer>`, hour, minute)

	// Weekly: add Weekday
	if containsStr(onCalendar, "Mon") {
		s += fmt.Sprintf(`
        <key>Weekday</key>
        <integer>1</integer>`)
	}

	// Monthly: add Day
	if containsStr(onCalendar, "*-*-01") {
		s += fmt.Sprintf(`
        <key>Day</key>
        <integer>1</integer>`)
	}

	s += "\n    </dict>"
	return s
}


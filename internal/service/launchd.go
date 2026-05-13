// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Launchd implements Manager for macOS.
type Launchd struct{}

func (l *Launchd) Name() string { return "launchd" }

const (
	syncthingLabel = "ch.corbet.dotkeeper.syncthing"
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

func (l *Launchd) DaemonReload() error {
	return nil // launchd doesn't need this
}

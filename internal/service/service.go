// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package service abstracts platform-specific service and timer management.
// Supports systemd (Linux), launchd (macOS), Task Scheduler (Windows),
// and cron (BSD/fallback).
package service

// Manager handles installing and managing background services and timers.
type Manager interface {
	// Name returns the platform backend name (e.g. "systemd", "launchd").
	Name() string

	// InstallSyncthing installs and starts the Syncthing background service.
	InstallSyncthing(binaryPath string) error

	// InstallTimer installs a periodic timer/schedule for git backup.
	// onCalendar uses systemd OnCalendar syntax; backends convert as needed.
	InstallTimer(binaryPath, configPath, onCalendar string) error

	// StartSyncthing starts the Syncthing service.
	StartSyncthing() error

	// StopSyncthing stops the Syncthing service.
	StopSyncthing() error

	// IsSyncthingRunning returns true if the Syncthing service is active.
	IsSyncthingRunning() bool

	// IsTimerActive returns true if the git backup timer is active.
	IsTimerActive() bool

	// DaemonReload reloads the service manager config (no-op on some platforms).
	DaemonReload() error
}

// PlatformName returns the backend name from any Manager.
func PlatformName(m Manager) string {
	return m.Name()
}

// NoopManager is a fallback that does nothing. Used when platform detection fails.
type NoopManager struct{}

func (n *NoopManager) Name() string                                        { return "none" }
func (n *NoopManager) InstallSyncthing(binaryPath string) error            { return nil }
func (n *NoopManager) InstallTimer(binaryPath, configPath, onCalendar string) error { return nil }
func (n *NoopManager) StartSyncthing() error                               { return nil }
func (n *NoopManager) StopSyncthing() error                                { return nil }
func (n *NoopManager) IsSyncthingRunning() bool                            { return false }
func (n *NoopManager) IsTimerActive() bool                                 { return false }
func (n *NoopManager) DaemonReload() error                                 { return nil }

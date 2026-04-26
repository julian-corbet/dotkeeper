// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package service abstracts platform-specific service management for the
// embedded Syncthing daemon. Supports systemd (Linux), launchd (macOS),
// Task Scheduler (Windows), and cron (BSD/fallback).
//
// In v0.5, git backups run inside the Syncthing service via the reconcile
// daemon, so there is no separate periodic timer to manage.
package service

import "time"

// Manager handles installing and managing the Syncthing background service.
type Manager interface {
	// Name returns the platform backend name (e.g. "systemd", "launchd").
	Name() string

	// InstallSyncthing installs and starts the Syncthing background service.
	InstallSyncthing(binaryPath string) error

	// StartSyncthing starts the Syncthing service.
	StartSyncthing() error

	// StopSyncthing stops the Syncthing service.
	StopSyncthing() error

	// IsSyncthingRunning returns true if the Syncthing service is active.
	IsSyncthingRunning() bool

	// DaemonReload reloads the service manager config (no-op on some platforms).
	DaemonReload() error
}

// SyncthingUnitStatus is a richer view of the Syncthing service than
// the boolean IsSyncthingRunning — used by `dotkeeper doctor` to
// distinguish "inactive" (user never started it) from "failed"
// (systemd saw it crash).
type SyncthingUnitStatus struct {
	// Active is the unit ActiveState — typically one of:
	//   active, inactive, failed, activating, deactivating, unknown.
	// Platforms without a service manager return "unknown".
	Active string
	// Sub is the SubState (e.g. "running", "dead", "failed"). Empty when
	// the backend cannot provide it.
	Sub string
	// Since is the timestamp of the last state transition, if known.
	// Zero value means the timestamp was not available.
	Since time.Time
}

// PlatformName returns the backend name from any Manager.
func PlatformName(m Manager) string {
	return m.Name()
}

// NoopManager is a fallback that does nothing. Used when platform detection fails.
type NoopManager struct{}

func (n *NoopManager) Name() string                             { return "none" }
func (n *NoopManager) InstallSyncthing(binaryPath string) error { return nil }
func (n *NoopManager) StartSyncthing() error                    { return nil }
func (n *NoopManager) StopSyncthing() error                     { return nil }
func (n *NoopManager) IsSyncthingRunning() bool                 { return false }
func (n *NoopManager) DaemonReload() error                      { return nil }

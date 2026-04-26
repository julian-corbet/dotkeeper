// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package service

import (
	"fmt"
	"os/exec"
)

// WinTaskSched implements Manager using Windows Task Scheduler (schtasks).
type WinTaskSched struct{}

func (w *WinTaskSched) Name() string { return "Task Scheduler" }

const (
	syncthingTaskName = "dotkeeper-syncthing"
)

func (w *WinTaskSched) InstallSyncthing(binaryPath string) error {
	// Create a task that runs at logon and restarts on failure
	err := exec.Command("schtasks", "/Create", "/F",
		"/TN", syncthingTaskName,
		"/TR", fmt.Sprintf(`"%s" start`, binaryPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
	).Run()
	if err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}
	// Start it now
	return exec.Command("schtasks", "/Run", "/TN", syncthingTaskName).Run()
}

func (w *WinTaskSched) StartSyncthing() error {
	return exec.Command("schtasks", "/Run", "/TN", syncthingTaskName).Run()
}

func (w *WinTaskSched) StopSyncthing() error {
	return exec.Command("schtasks", "/End", "/TN", syncthingTaskName).Run()
}

func (w *WinTaskSched) IsSyncthingRunning() bool {
	out, err := exec.Command("schtasks", "/Query", "/TN", syncthingTaskName, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return containsStr(string(out), "Running")
}

func (w *WinTaskSched) DaemonReload() error {
	return nil // Windows doesn't need this
}

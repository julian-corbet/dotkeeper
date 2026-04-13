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
	syncTaskName      = "dotkeeper-sync"
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

func (w *WinTaskSched) InstallTimer(binaryPath, configPath, onCalendar string) error {
	// Convert schedule to schtasks format
	schedule, modifier := calendarToSchtasks(onCalendar)

	args := []string{"/Create", "/F",
		"/TN", syncTaskName,
		"/TR", fmt.Sprintf(`"%s" sync`, binaryPath),
		"/SC", schedule,
		"/RL", "LIMITED",
	}
	if modifier != "" {
		args = append(args, "/MO", modifier)
	}

	return exec.Command("schtasks", args...).Run()
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

func (w *WinTaskSched) IsTimerActive() bool {
	out, err := exec.Command("schtasks", "/Query", "/TN", syncTaskName, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return containsStr(string(out), "Ready") || containsStr(string(out), "Running")
}

func (w *WinTaskSched) DaemonReload() error {
	return nil // Windows doesn't need this
}

func calendarToSchtasks(onCalendar string) (schedule, modifier string) {
	switch {
	case containsStr(onCalendar, "Mon"):
		return "WEEKLY", ""
	case containsStr(onCalendar, "*-*-01"):
		return "MONTHLY", ""
	case containsStr(onCalendar, "0/2:"):
		return "HOURLY", "2"
	case containsStr(onCalendar, "0/3:"):
		return "HOURLY", "3"
	case containsStr(onCalendar, "0/4:"):
		return "HOURLY", "4"
	case containsStr(onCalendar, "0/6:"):
		return "HOURLY", "6"
	case containsStr(onCalendar, "0/8:"):
		return "HOURLY", "8"
	case containsStr(onCalendar, "0/12:"):
		return "HOURLY", "12"
	case containsStr(onCalendar, "*-*-*"):
		return "DAILY", ""
	default:
		return "HOURLY", ""
	}
}

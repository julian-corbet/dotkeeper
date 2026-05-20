// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package procnice

import "os/exec"

// Run is a no-op wrapper around cmd.Run() on non-Linux platforms.
// The cgroup-level limits in the systemd unit do not apply, and there
// is no portable way to set ionice on macOS/Windows from Go without a
// shell-out. Callers get unchanged behavior.
func Run(cmd *exec.Cmd) error { return cmd.Run() }

func Output(cmd *exec.Cmd) ([]byte, error) { return cmd.Output() }

func CombinedOutput(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }

// LowerSelf is a no-op on non-Linux platforms. setpriority(2) exists
// on the BSDs and macOS but ioprio_set(2) is Linux-specific, and the
// daemon's deployment story on those platforms is not via systemd in
// any case — operators are expected to use launchd / rc.d nice
// settings, container scheduling, or accept default priority.
func LowerSelf() {}

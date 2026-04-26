// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin && !windows

package service

import (
	"fmt"
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/testutil"
)

// Golden file tests snapshot the exact output of service unit generation.
// Any accidental format change breaks the golden file.
//
// To update golden files after intentional changes:
//   go test -update ./internal/service/
//
// To add a new golden test:
//   1. Write a TestGolden* function below
//   2. Run: go test -update -run TestGoldenNewTest ./internal/service/
//   3. Review the generated .golden file
//   4. Commit both the test and the golden file

// --- Systemd unit golden tests ---
// These test the string format without actually calling systemctl.

func TestGoldenSystemdSyncthingUnit(t *testing.T) {
	got := fmt.Sprintf(`[Unit]
Description=dotkeeper embedded Syncthing instance
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="%s" start
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, "/usr/local/bin/dotkeeper")
	testutil.GoldenCheck(t, "systemd_syncthing_unit", got)
}

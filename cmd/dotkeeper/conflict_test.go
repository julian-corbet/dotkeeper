// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// TestDeviceShortToHostname covers the mapping from the 7-char short
// form (as embedded in Syncthing conflict filenames) to the friendly
// hostname registered in the shared config.
func TestDeviceShortToHostname(t *testing.T) {
	cfg := &config.SharedConfig{
		Machines: map[string]config.MachineEntry{
			"desktop": {
				Hostname:    "CACHYOS-Desktop",
				Slot:        0,
				SyncthingID: "UUS6FSQ-ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ23-4567ABC-DEFGHIJ-KLMNOPQ",
			},
			"laptop": {
				Hostname:    "CORBET-ELITEBOOK",
				Slot:        1,
				SyncthingID: "WB25TET-ZYXWVUT-SRQPONM-LKJIHGF-EDCBA76-5432ZYX-WVUTSRQ-PONMLKJ",
			},
			// No SyncthingID yet (freshly-joined, pre-cert-propagation).
			"unknown": {
				Hostname:    "brand-new",
				Slot:        2,
				SyncthingID: "",
			},
		},
	}

	got := deviceShortToHostname(cfg)

	if got["UUS6FSQ"] != "CACHYOS-Desktop" {
		t.Errorf("UUS6FSQ = %q, want %q", got["UUS6FSQ"], "CACHYOS-Desktop")
	}
	if got["WB25TET"] != "CORBET-ELITEBOOK" {
		t.Errorf("WB25TET = %q, want %q", got["WB25TET"], "CORBET-ELITEBOOK")
	}
	// Entries without an ID must not pollute the map (empty key would
	// be returned for every unknown lookup).
	if _, ok := got[""]; ok {
		t.Error("entry with empty SyncthingID should be skipped, not mapped under empty key")
	}
	// An unknown short ID returns the zero value so callers can fall
	// back to the raw short form.
	if host, ok := got["ZZZZZZZ"]; ok {
		t.Errorf("unknown short ID should not be in map, got %q", host)
	}
}

// TestDeviceShortToHostnameEmptyConfig covers the degenerate case where
// the config has no machines registered yet (e.g. very first boot).
func TestDeviceShortToHostnameEmptyConfig(t *testing.T) {
	cfg := &config.SharedConfig{Machines: map[string]config.MachineEntry{}}
	got := deviceShortToHostname(cfg)
	if len(got) != 0 {
		t.Errorf("empty config should yield empty map, got %d entries", len(got))
	}
}

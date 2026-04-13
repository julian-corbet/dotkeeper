// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"testing"
)

func TestDetect(t *testing.T) {
	mgr, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if mgr == nil {
		t.Fatal("Detect returned nil")
	}

	name := mgr.Name()
	if name == "" {
		t.Error("Manager.Name() returned empty string")
	}
	t.Logf("detected service manager: %s", name)
}

func TestPlatformName(t *testing.T) {
	mgr, _ := Detect()
	name := PlatformName(mgr)
	if name == "" {
		t.Error("PlatformName returned empty")
	}
	// Should match Name()
	if name != mgr.Name() {
		t.Errorf("PlatformName = %q, mgr.Name() = %q", name, mgr.Name())
	}
}

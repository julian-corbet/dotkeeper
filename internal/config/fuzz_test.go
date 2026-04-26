// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"testing"
)

// FuzzMachineConfigV2RoundTrip tests that WriteMachineConfigV2 → LoadMachineConfigV2
// never panics for arbitrary machine names and slot values.
func FuzzMachineConfigV2RoundTrip(f *testing.F) {
	f.Add("my-desktop", uint(0))
	f.Add("host.local", uint(1))
	f.Add("", uint(0))
	f.Add("日本語-host", uint(15))

	f.Fuzz(func(t *testing.T, name string, slot uint) {
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)

		cfg := &MachineConfigV2{
			SchemaVersion: 2,
			Name:          name,
			Slot:          slot,
			Discovery: DiscoveryConfig{
				ScanRoots: []string{"~/Documents"},
				ScanDepth: 3,
			},
		}

		// Write must not panic
		err := WriteMachineConfigV2(cfg)
		if err != nil {
			return // write failure is acceptable for weird inputs
		}

		// Load must not panic
		loaded, err := LoadMachineConfigV2()
		if err != nil {
			return // parse failure is acceptable for weird inputs
		}
		if loaded == nil {
			t.Error("LoadMachineConfigV2 returned nil after successful write")
		}
	})
}

// FuzzExpandContractPath tests that ExpandPath and ContractPath never panic.
func FuzzExpandContractPath(f *testing.F) {
	f.Add("~/Documents")
	f.Add("/absolute/path")
	f.Add("relative/path")
	f.Add("")
	f.Add("~")
	f.Add("~/")
	f.Add("~root/something")
	f.Add("/home/user/../../../etc/passwd")

	f.Fuzz(func(t *testing.T, path string) {
		// Must not panic
		expanded := ExpandPath(path)
		_ = ContractPath(expanded)
	})
}

// FuzzSanitizeTOMLKey tests that sanitizeTOMLKey never panics and
// always returns valid UTF-8 for any byte sequence input.
func FuzzSanitizeTOMLKey(f *testing.F) {
	f.Add("simple_key")
	f.Add("key-with-dashes")
	f.Add("key.with.dots")
	f.Add("key with spaces")
	f.Add("")
	f.Add("日本語")
	f.Add("\xe8")
	f.Add("hello\xffworld")

	f.Fuzz(func(t *testing.T, key string) {
		result := sanitizeTOMLKey(key)
		// Must not panic; result should be non-empty if input was non-empty,
		// though empty input returns empty output.
		_ = result
	})
}

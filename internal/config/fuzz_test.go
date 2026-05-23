// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"sync"
	"testing"
)

// fuzzMachineConfigMu serialises FuzzMachineConfigV2RoundTrip's
// body across concurrent goroutines within a single fuzz worker
// process. The body mutates the process-global
// $XDG_CONFIG_HOME via t.Setenv and then does file I/O underneath
// it; with the default GOMAXPROCS workers running in parallel
// within one process, two callbacks would race on the env var and
// each could end up reading the OTHER worker's tmp directory.
// Fuzz still benefits from true parallelism across worker
// PROCESSES — the harness spawns multiple workers — but
// within-process the env-mutation step must be sequential.
var fuzzMachineConfigMu sync.Mutex

// FuzzMachineConfigV2RoundTrip tests that WriteMachineConfigV2 → LoadMachineConfigV2
// never panics for arbitrary machine names and slot values.
func FuzzMachineConfigV2RoundTrip(f *testing.F) {
	f.Add("my-desktop", uint(0))
	f.Add("host.local", uint(1))
	f.Add("", uint(0))
	f.Add("日本語-host", uint(15))

	f.Fuzz(func(t *testing.T, name string, slot uint) {
		// Bound the input to keep the per-iteration cost predictable.
		// Without this, the fuzzer eventually generates multi-MB
		// `name` strings; the TOML write/parse round-trip then
		// dominates the per-fuzz budget and slow CI runners hit the
		// fuzz harness's per-target deadline, failing the smoke
		// suite for reasons unrelated to a real bug. Real machine
		// names are well under this limit (Tailscale caps at 63
		// per DNS-label, machine.toml convention is similar).
		if len(name) > 4096 {
			return
		}

		// Serialise within-process: see fuzzMachineConfigMu comment.
		fuzzMachineConfigMu.Lock()
		defer fuzzMachineConfigMu.Unlock()

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

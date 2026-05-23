// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Benchmarks for config-layer hot paths called on every reconcile
// or commit. See internal/transport/bench_test.go for the
// regression-catching rationale.
//
// Target order-of-magnitude (Intel-class laptop, 2026 baselines):
//
//	BenchmarkLoadMachineConfigV2       <200 µs/op
//	BenchmarkMergeSyncIgnorePatterns   <50  µs/op
//	BenchmarkSyncIgnoreFileContent     <100 µs/op
//
// Run with: go test -tags noassets -bench=. -benchtime=2s ./internal/config/

func BenchmarkLoadMachineConfigV2(b *testing.B) {
	tmp := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", tmp)
	// Write a realistic machine.toml — 2 peers, 3 scan roots.
	cfg := &MachineConfigV2{
		SchemaVersion: 2,
		Name:          "bench-host",
		Slot:          1,
		Discovery: DiscoveryConfig{
			ScanRoots: []string{"~/Documents/GitHub", "~/.agent", "~/code"},
			ScanDepth: 3,
		},
		Peers: []PeerEntry{
			{Name: "desktop", DeviceID: strings.Repeat("ABCDEFG-", 7) + "ABCDEFG"},
			{Name: "laptop", DeviceID: strings.Repeat("HIJKLMN-", 7) + "HIJKLMN"},
		},
	}
	if err := WriteMachineConfigV2(cfg); err != nil {
		b.Fatalf("WriteMachineConfigV2: %v", err)
	}
	// Confirm the file exists where the loader will look.
	if _, err := os.Stat(filepath.Join(tmp, "dotkeeper", "machine.toml")); err != nil {
		b.Fatalf("setup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadMachineConfigV2(); err != nil {
			b.Fatalf("LoadMachineConfigV2: %v", err)
		}
	}
}

func BenchmarkMergeSyncIgnorePatterns(b *testing.B) {
	extra := []string{"my-custom-cache", "*.tmp.bak", "build-output"}
	for i := 0; i < b.N; i++ {
		_ = MergeSyncIgnorePatterns(extra)
	}
}

func BenchmarkSyncIgnoreFileContent(b *testing.B) {
	extra := []string{"my-custom-cache", "*.tmp.bak", "build-output"}
	for i := 0; i < b.N; i++ {
		_ = SyncIgnoreFileContent(extra)
	}
}

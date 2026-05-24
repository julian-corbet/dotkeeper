// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// MachineConfigV2 is the v0.5 schema for $XDG_CONFIG_HOME/dotkeeper/machine.toml.
// This file is declarative, non-secret, and may be authored by Nix/home-manager
// or edited directly by the user. It never contains secrets.
// Corresponds to ADR 0002 and ADR 0004.
type MachineConfigV2 struct {
	SchemaVersion int `toml:"schema_version"`

	// Name is this machine's name in the mesh (e.g. "laptop", "desktop").
	Name string `toml:"name"`
	// Slot is the staggered git-backup slot (0-based, unique per machine).
	Slot uint `toml:"slot"`

	// DefaultCommitPolicy is inherited by repos that don't override it.
	// Allowed values: "manual", "on-idle", "timer". Default: "manual".
	DefaultCommitPolicy string `toml:"default_commit_policy"`
	// DefaultGitInterval is the default git backup frequency (e.g. "hourly",
	// "daily", "2h"). Default: "daily".
	DefaultGitInterval string `toml:"default_git_interval"`
	// DefaultSlotOffsetMinutes is the stagger offset in minutes. Default: 5.
	DefaultSlotOffsetMinutes uint `toml:"default_slot_offset_minutes"`
	// DefaultShareWith is the list of peer device names to share newly-discovered
	// repos with by default. Empty means share with all peers.
	DefaultShareWith []string `toml:"default_share_with"`

	// Peers is an optional declarative peer roster. It is safe for
	// home-manager/Nix to generate because Syncthing device IDs are public
	// identities, not private keys. Imperative peer commands may still add
	// peers to state.toml; callers merge both sources.
	Peers []PeerEntry `toml:"peers"`

	// Discovery configures how dotkeeper finds managed repos on this machine.
	Discovery DiscoveryConfig `toml:"discovery"`

	// ReconcileInterval is how often the reconciler runs as a safety net.
	// Default: "5m".
	ReconcileInterval string `toml:"reconcile_interval"`

	// Debug holds knobs that are useful for performance investigation
	// but disabled by default. Production installs leave Debug at its
	// zero value; operators who need a profile flip the field they
	// want, restart, capture, and revert. Never persisted by dotkeeper
	// itself — only ever set by the operator.
	Debug DebugConfig `toml:"debug"`
}

// DebugConfig holds opt-in observability surfaces.
type DebugConfig struct {
	// PprofAddress, when non-empty, makes the daemon expose the Go
	// runtime's standard /debug/pprof/* endpoints on the named TCP
	// address (typically "127.0.0.1:6060"). Useful for capturing CPU,
	// heap, goroutine, mutex, and block profiles with `go tool pprof
	// http://...`. Off by default because:
	//   - The endpoints expose goroutine stack traces, which can leak
	//     paths and other internal state.
	//   - Profiling itself perturbs the workload (CPU sampling at
	//     100 Hz, heap-allocation tracking, etc.), so it should not
	//     run during steady-state operation.
	//   - Binding any port is one more thing to fail.
	// Use 127.0.0.1:* explicitly — dotkeeper does not enforce the
	// loopback restriction, but exposing pprof externally is a
	// well-known footgun.
	PprofAddress string `toml:"pprof_address"`
}

// DiscoveryConfig configures scan-root-based repo discovery (ADR 0004).
type DiscoveryConfig struct {
	// ScanRoots are the directories dotkeeper walks looking for .dotkeeper.toml
	// files. Tilde-prefixed paths are expanded. Default: ["~/Documents/GitHub"].
	ScanRoots []string `toml:"scan_roots"`
	// Exclude is a list of paths to skip during discovery.
	Exclude []string `toml:"exclude"`
	// ScanInterval controls how often the discovery scan runs. Default: "5m".
	ScanInterval string `toml:"scan_interval"`
	// ScanDepth is the maximum directory depth to walk under each scan root.
	// Default: 3.
	ScanDepth int `toml:"scan_depth"`
}

// applyMachineV2Defaults fills in zero-value fields with their documented defaults.
func applyMachineV2Defaults(cfg *MachineConfigV2) {
	if cfg.DefaultCommitPolicy == "" {
		cfg.DefaultCommitPolicy = "manual"
	}
	if cfg.DefaultGitInterval == "" {
		cfg.DefaultGitInterval = "daily"
	}
	if cfg.DefaultSlotOffsetMinutes == 0 {
		cfg.DefaultSlotOffsetMinutes = 5
	}
	if len(cfg.Discovery.ScanRoots) == 0 {
		cfg.Discovery.ScanRoots = []string{"~/Documents/GitHub"}
	}
	if cfg.Discovery.ScanInterval == "" {
		cfg.Discovery.ScanInterval = "5m"
	}
	if cfg.Discovery.ScanDepth == 0 {
		cfg.Discovery.ScanDepth = 3
	}
	if cfg.ReconcileInterval == "" {
		cfg.ReconcileInterval = "5m"
	}
	if cfg.DefaultShareWith == nil {
		cfg.DefaultShareWith = []string{}
	}
	if cfg.Peers == nil {
		cfg.Peers = []PeerEntry{}
	}
	if cfg.Discovery.Exclude == nil {
		cfg.Discovery.Exclude = []string{}
	}
}

// LoadMachineConfigV2 reads machine.toml from the XDG config directory and
// applies defaults for any omitted fields. Returns nil (no error) if the file
// does not yet exist.
func LoadMachineConfigV2() (*MachineConfigV2, error) {
	path := MachineConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var cfg MachineConfigV2
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	applyMachineV2Defaults(&cfg)
	return &cfg, nil
}

// WriteMachineConfigV2 serialises cfg to machine.toml. The config directory is
// created with mode 0700 if it does not exist. The file is written with mode
// 0600.
func WriteMachineConfigV2(cfg *MachineConfigV2) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# dotkeeper machine config (v2) — declarative, non-secret\n")
	b.WriteString("# Safe to author by hand or generate via Nix/home-manager.\n\n")

	fmt.Fprintf(&b, "schema_version = %d\n", cfg.SchemaVersion)
	fmt.Fprintf(&b, "name = %q\n", cfg.Name)
	fmt.Fprintf(&b, "slot = %d\n", cfg.Slot)
	fmt.Fprintf(&b, "default_commit_policy = %q\n", cfg.DefaultCommitPolicy)
	fmt.Fprintf(&b, "default_git_interval = %q\n", cfg.DefaultGitInterval)
	fmt.Fprintf(&b, "default_slot_offset_minutes = %d\n", cfg.DefaultSlotOffsetMinutes)
	fmt.Fprintf(&b, "reconcile_interval = %q\n", cfg.ReconcileInterval)

	b.WriteString("default_share_with = [")
	for i, s := range cfg.DefaultShareWith {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", s)
	}
	b.WriteString("]\n")

	if len(cfg.Peers) > 0 {
		b.WriteString("\n")
		for _, p := range cfg.Peers {
			b.WriteString("[[peers]]\n")
			fmt.Fprintf(&b, "name = %q\n", p.Name)
			fmt.Fprintf(&b, "device_id = %q\n", p.DeviceID)
			if p.LearnedAt.IsZero() {
				fmt.Fprintf(&b, "learned_at = %s\n", p.LearnedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
			} else {
				fmt.Fprintf(&b, "learned_at = %s\n", p.LearnedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n[discovery]\n")
	b.WriteString("scan_roots = [\n")
	for _, r := range cfg.Discovery.ScanRoots {
		fmt.Fprintf(&b, "    %q,\n", r)
	}
	b.WriteString("]\n")
	b.WriteString("exclude = [\n")
	for _, e := range cfg.Discovery.Exclude {
		fmt.Fprintf(&b, "    %q,\n", e)
	}
	b.WriteString("]\n")
	fmt.Fprintf(&b, "scan_interval = %q\n", cfg.Discovery.ScanInterval)
	fmt.Fprintf(&b, "scan_depth = %d\n", cfg.Discovery.ScanDepth)

	return WriteFileAtomic(MachineConfigPath(), []byte(b.String()), 0o600)
}

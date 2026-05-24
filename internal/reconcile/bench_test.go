// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"fmt"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// Benchmarks for the reconcile package's per-tick hot paths.
//
// Every reconcile tick runs:
//
//   - BuildDesired (machine.toml + per-repo .dotkeeper.toml + state.toml
//     → Desired). Linear in repo count × peer count.
//   - Diff (Desired vs Observed → Plan). Linear in repo count + folder
//     count + peer count.
//
// Both are called every 5 min on the safety net AND every fsnotify-
// driven trigger. The 30-repo / 5-peer scenario matches the user's
// actual scale + the hypothetical 5-machine fleet they referenced.
//
// Order-of-magnitude budget (Intel Core Ultra 7 258V, 2026):
//
//	BenchmarkDiffSteadyState30Repos     <250 µs/op  (all-aligned, no plan emit)
//	BenchmarkDiffColdStart30Repos       <200 µs/op  (every repo needs ADD)
//	BenchmarkDiffOneRepoChanged30Total  <250 µs/op  (typical: one repo dirty)
//	BenchmarkBuildDesired30Repos        <150 µs/op  (TOML parse not included)
//
// Budgets reflect the post-observedRepoByPath-fix baselines: ~190 µs
// steady-state on this machine. They are deliberately above
// measured values so noise (CI bench job, shared runners) doesn't
// turn the safety net into a flake source. Treat regressions
// approaching 2× of these numbers as a CPU-floor breach worth
// investigating — Diff runs every 5 min on the safety net AND on
// every fsnotify-driven trigger, so a 10× slowdown is the difference
// between "invisible" and "spinning fans".
//
// Run with: go test -tags noassets -bench=. -benchtime=2s ./internal/reconcile/

const (
	benchRepoCount = 30
	benchPeerCount = 5
)

// fixtureFor30Repos5Peers returns a Desired + Observed that mirrors
// the steady-state reconcile environment of a healthy fleet: every
// desired folder is observed, every observed folder is desired,
// every peer is paired, and no scheduler drift is present. Diff
// against this fixture emits zero actions and represents the cost
// the daemon pays every tick when nothing has changed — the most
// common case in practice.
func fixtureFor30Repos5Peers() (Desired, Observed) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	peers := make([]PeerDesired, benchPeerCount)
	peerIDs := make([]string, benchPeerCount)
	for i := 0; i < benchPeerCount; i++ {
		id := fmt.Sprintf("PEER-%02d-AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG", i)
		peers[i] = PeerDesired{
			Name:     fmt.Sprintf("peer-%02d", i),
			DeviceID: id,
		}
		peerIDs[i] = id
	}

	repos := make(map[string]RepoDesired, benchRepoCount)
	folders := make([]FolderObs, 0, benchRepoCount)
	trackedRepos := make([]RepoObs, 0, benchRepoCount)
	activity := make(map[string]time.Time, benchRepoCount)
	watchHealth := make(map[string]FolderHealth, benchRepoCount)
	lastRescans := make(map[string]time.Time, benchRepoCount)

	for i := 0; i < benchRepoCount; i++ {
		path := fmt.Sprintf("/home/bench/Documents/GitHub/repo-%02d", i)
		folderID := fmt.Sprintf("dk-repo-%02d-abc123", i)
		repos[path] = RepoDesired{
			Path:              path,
			SyncthingFolderID: folderID,
			Ignore:            []string{"build", "node_modules", "*.tmp"},
			ShareWith:         peerIDs,
			CommitPolicy:      "manual",
			GitInterval:       "daily",
		}
		folders = append(folders, FolderObs{
			SyncthingFolderID: folderID,
			Path:              path,
			Devices:           peerIDs,
			MarkerDirMissing:  false,
			// Matches stclient.CanonicalRescanIntervalS to keep
			// folderScheduleDrifted false.
			RescanIntervalS:  86400,
			FsWatcherEnabled: true,
		})
		trackedRepos = append(trackedRepos, RepoObs{
			Path:         path,
			HeadCommit:   "abc123",
			LastBackupAt: now.Add(-30 * time.Minute),
		})
		activity[path] = now.Add(-1 * time.Hour)
		watchHealth[path] = FolderHealth{FilesystemReliable: true}
		lastRescans[path] = now.Add(-6 * time.Hour)
	}

	livePeers := make([]LivePeer, benchPeerCount)
	for i, p := range peers {
		livePeers[i] = LivePeer{DeviceID: p.DeviceID}
	}

	d := Desired{
		MachineName: "bench-host",
		Repos:       repos,
		Peers:       peers,
	}
	o := Observed{
		ManagedFolders:     folders,
		TrackedRepos:       trackedRepos,
		LivePeers:          livePeers,
		LastActivityByPath: activity,
		WatchHealthByPath:  watchHealth,
		LastRescanByPath:   lastRescans,
		Now:                now,
	}
	return d, o
}

// BenchmarkDiffSteadyState30Repos covers the modal case: nothing has
// changed since the last reconcile. Plan is empty, but Diff still
// walks every desired folder + observed folder + repo to confirm
// alignment. Cost here is paid every 5 min on every machine.
func BenchmarkDiffSteadyState30Repos(b *testing.B) {
	d, o := fixtureFor30Repos5Peers()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Diff(d, o)
	}
}

// BenchmarkDiffColdStart30Repos models the first reconcile after a
// fresh daemon start where Syncthing has no folders yet. Every
// desired folder produces an AddSyncthingFolder + every desired peer
// produces an AddSyncthingDevice — the maximum-emit case.
func BenchmarkDiffColdStart30Repos(b *testing.B) {
	d, o := fixtureFor30Repos5Peers()
	// Empty the observed side: nothing exists yet.
	o.ManagedFolders = nil
	o.TrackedRepos = nil
	o.LivePeers = nil
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Diff(d, o)
	}
}

// BenchmarkDiffOneRepoChanged30Total models the typical fsnotify-
// driven reconcile: 29 repos still aligned, one repo dirty and due
// for backup. Plan is one GitCommitDirty + one GitPushRepo. This
// represents real-world incremental cost.
func BenchmarkDiffOneRepoChanged30Total(b *testing.B) {
	d, o := fixtureFor30Repos5Peers()
	// Promote first tracked repo to dirty + give it a long-enough
	// LastChangeAt that the on-idle policy returns due.
	for path, r := range d.Repos {
		r.CommitPolicy = "on-idle"
		r.IdleSeconds = 60
		d.Repos[path] = r
		break
	}
	for i := range o.TrackedRepos {
		if i == 0 {
			o.TrackedRepos[i].IsDirty = true
			o.TrackedRepos[i].LastChangeAt = o.Now.Add(-10 * time.Minute)
			break
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Diff(d, o)
	}
}

// BenchmarkBuildDesired30Repos measures the cost of translating the
// parsed-config layer (MachineConfigV2 + per-repo RepoConfigV2 map)
// into the Diff-ready Desired struct. TOML parse cost is excluded
// (already covered by config.BenchmarkLoadMachineConfigV2); this
// benchmark isolates the merge + share-with resolution.
func BenchmarkBuildDesired30Repos(b *testing.B) {
	machine := &config.MachineConfigV2{
		Name:                "bench-host",
		DefaultCommitPolicy: "manual",
		DefaultGitInterval:  "daily",
		Peers:               make([]config.PeerEntry, benchPeerCount),
	}
	for i := 0; i < benchPeerCount; i++ {
		machine.Peers[i] = config.PeerEntry{
			Name:     fmt.Sprintf("peer-%02d", i),
			DeviceID: fmt.Sprintf("PEER-%02d-AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG", i),
		}
	}
	repos := make(map[string]*config.RepoConfigV2, benchRepoCount)
	for i := 0; i < benchRepoCount; i++ {
		path := fmt.Sprintf("/home/bench/Documents/GitHub/repo-%02d", i)
		repos[path] = &config.RepoConfigV2{
			SchemaVersion: 2,
			Sync: config.RepoSyncConfig{
				SyncthingFolderID: fmt.Sprintf("dk-repo-%02d-abc123", i),
				Ignore:            []string{"build", "node_modules", "*.tmp"},
				ShareWith:         []string{}, // share with all peers
			},
		}
	}
	state := &config.StateV2{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildDesired(machine, repos, state)
	}
}

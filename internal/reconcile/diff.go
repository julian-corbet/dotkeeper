// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
package reconcile

import (
	"sort"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// Diff computes the Plan needed to move observed state towards desired state.
// It is a pure function: given the same inputs it always returns the same
// output, and it has no side effects. Callers may invoke it repeatedly without
// risk of drift.
//
// Folder reconciliation:
//   - Folders in Desired but not Observed → AddSyncthingFolder
//   - Folders in Observed but not Desired → RemoveSyncthingFolder
//   - Folders in both but with differing device lists → UpdateSyncthingFolderDevices
//
// Peer reconciliation:
//   - Desired peer missing from Syncthing → AddSyncthingDevice
//
// Repo reconciliation honours RepoDesired.CommitPolicy:
//   - manual: no automatic git action
//   - on-idle: commit/push after the dirty tree has been quiet long enough
//   - timer: commit/push once the configured interval is due
func Diff(desired Desired, observed Observed) Plan {
	var plan Plan

	// Add missing peers first so subsequent folder actions can reference them.
	obsPeers := make(map[string]bool, len(observed.LivePeers))
	for _, p := range observed.LivePeers {
		obsPeers[p.DeviceID] = true
	}
	for _, p := range desired.Peers {
		if p.DeviceID == "" || obsPeers[p.DeviceID] {
			continue
		}
		plan = append(plan, AddSyncthingDevice(p))
	}

	// Index observed folders by SyncthingFolderID for O(1) lookup.
	obsFolders := make(map[string]FolderObs, len(observed.ManagedFolders))
	for _, f := range observed.ManagedFolders {
		obsFolders[f.SyncthingFolderID] = f
	}

	// Index desired repos by SyncthingFolderID.
	type desiredFolder struct {
		folderID string
		path     string
		devices  []string
	}
	var desiredFolders []desiredFolder
	for _, r := range desired.Repos {
		if r.SyncthingFolderID == "" {
			continue
		}
		desiredFolders = append(desiredFolders, desiredFolder{
			folderID: r.SyncthingFolderID,
			path:     r.Path,
			devices:  r.ShareWith,
		})
	}
	// Sort for deterministic output.
	sort.Slice(desiredFolders, func(i, j int) bool {
		return desiredFolders[i].folderID < desiredFolders[j].folderID
	})

	desiredFolderIndex := make(map[string]desiredFolder, len(desiredFolders))
	for _, df := range desiredFolders {
		desiredFolderIndex[df.folderID] = df
	}

	// Emit add/update actions for desired folders.
	for _, df := range desiredFolders {
		obs, exists := obsFolders[df.folderID]
		if !exists {
			plan = append(plan, AddSyncthingFolder{
				FolderID: df.folderID,
				Path:     df.path,
				Devices:  df.devices,
			})
		} else if !stringSlicesEqual(sortedCopy(obs.Devices), sortedCopy(df.devices)) {
			plan = append(plan, UpdateSyncthingFolderDevices{
				FolderID: df.folderID,
				Devices:  df.devices,
			})
		}
		// Scheduler-field drift check: independent of device drift,
		// because a folder can have the right devices but the wrong
		// scheduler values (the case after upgrading from a release
		// that wrote rescanIntervalS=60). Emitted on the existing-
		// folder branch only; new folders get the canonical values
		// directly from AddSyncthingFolder/AddOrUpdateFolder.
		if exists && folderScheduleDrifted(obs) {
			plan = append(plan, UpdateSyncthingFolderSchedule{
				FolderID: df.folderID,
			})
		}
		if exists && obs.MarkerDirMissing {
			plan = append(plan, EnsureFolderMarker{RepoPath: df.path})
		}
		// Auto-pause: emit Pause/Unpause based on observed activity.
		// Skipped when LastActivityByPath is nil (tracker unavailable,
		// e.g. on first-boot before discovery has populated paths or
		// when the daemon was built without activity wiring).
		if exists && observed.LastActivityByPath != nil {
			if act := autoPauseAction(obs, df.path, observed.LastActivityByPath, observed.Now); act != nil {
				plan = append(plan, act)
			}
		}
		// Smart rescan: emit RescanFolderNow when the watch health
		// or sleep state implies the fsWatcher missed something.
		// Paused folders are never rescanned — Syncthing rejects
		// scan requests for paused folders and the user-visible
		// effect (wait for unpause) is already correct.
		if exists && !obs.Paused {
			if act := smartRescanAction(obs, df.path, observed); act != nil {
				plan = append(plan, act)
			}
		}
		repoDesired := desired.Repos[df.path]
		repoObs := observedRepoByPath(observed.TrackedRepos, df.path)
		wantIgnore := config.SyncIgnoreFileContent(repoDesired.Ignore)
		if repoObs.IgnoreFileContent != wantIgnore {
			plan = append(plan, EnsureIgnoreFile{
				RepoPath: df.path,
				Patterns: repoDesired.Ignore,
			})
		}
	}

	// Emit remove actions for observed folders not in desired.
	// Sort for deterministic output.
	sortedObsFolders := make([]FolderObs, len(observed.ManagedFolders))
	copy(sortedObsFolders, observed.ManagedFolders)
	sort.Slice(sortedObsFolders, func(i, j int) bool {
		return sortedObsFolders[i].SyncthingFolderID < sortedObsFolders[j].SyncthingFolderID
	})
	for _, obs := range sortedObsFolders {
		if _, wanted := desiredFolderIndex[obs.SyncthingFolderID]; !wanted {
			plan = append(plan, RemoveSyncthingFolder{FolderID: obs.SyncthingFolderID})
		}
	}

	// Repo reconciliation: sort observed repos for deterministic output.
	sortedRepos := make([]RepoObs, len(observed.TrackedRepos))
	copy(sortedRepos, observed.TrackedRepos)
	sort.Slice(sortedRepos, func(i, j int) bool {
		return sortedRepos[i].Path < sortedRepos[j].Path
	})

	for _, repo := range sortedRepos {
		repoDesired, managed := desired.DesiredForPath(repo.Path)
		if !managed || repoDesired.CommitPolicy == "manual" || repoDesired.slotSkipped() {
			continue
		}

		due := repoBackupDue(repoDesired, repo, time.Now())
		// Defer this tick's backup if the user is mid-rebase / mid-merge /
		// mid-cherry-pick / mid-bisect. Slot timing is not "skipped" — the
		// next reconcile to observe a quiet repo fires the backup, still
		// within the configured interval. See queryRepoGitState.
		if due && repo.DevWorkflowActive {
			continue
		}
		if repo.IsDirty && due {
			plan = append(plan, GitCommitDirty{
				RepoPath: repo.Path,
				Message:  "auto: scheduled backup",
			})
		}
		if repo.HeadCommit != "" && due {
			// Only push if HEAD differs from the last successfully pushed commit
			// recorded in state.toml. This avoids redundant pushes when the repo
			// is already up-to-date on the remote.
			alreadyPushed := false
			if observed.CachedState != nil {
				if obs, ok := observed.CachedState.ObservedRepos[repo.Path]; ok {
					alreadyPushed = obs.LastPushedCommit == repo.HeadCommit
				}
			}
			if !alreadyPushed {
				plan = append(plan, GitPushRepo{RepoPath: repo.Path})
			}
		}
	}

	return plan
}

func observedRepoByPath(repos []RepoObs, path string) RepoObs {
	for _, repo := range repos {
		if repo.Path == path {
			return repo
		}
	}
	return RepoObs{Path: path}
}

func repoBackupDue(desired RepoDesired, observed RepoObs, now time.Time) bool {
	switch desired.CommitPolicy {
	case "on-idle":
		idleFor := 5 * time.Minute
		if desired.IdleSeconds > 0 {
			idleFor = time.Duration(desired.IdleSeconds) * time.Second
		}
		if observed.IsDirty {
			if observed.LastChangeAt.IsZero() {
				return true
			}
			return !observed.LastChangeAt.After(now.Add(-idleFor))
		}
		return true
	case "timer":
		interval := parseGitInterval(desired.GitInterval)
		if interval <= 0 {
			interval = time.Hour
		}
		if observed.LastBackupAt.IsZero() {
			return true
		}
		return !observed.LastBackupAt.Add(interval).After(now)
	default:
		return false
	}
}

func parseGitInterval(raw string) time.Duration {
	switch raw {
	case "hourly":
		return time.Hour
	case "", "daily":
		return 24 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "monthly":
		return 30 * 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return d
}

// sortedCopy returns a sorted copy of s without modifying the original.
func sortedCopy(s []string) []string {
	c := make([]string, len(s))
	copy(c, s)
	sort.Strings(c)
	return c
}

// stringSlicesEqual reports whether two pre-sorted string slices are equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RescanBackstopReliable is the maximum interval between rescans on
// a filesystem classified as reliable. Even when no overflow / wake
// event has fired, we re-scan once a week to cover detector blind
// spots — kernel bugs that don't surface as IN_Q_OVERFLOW, a peer
// that silently rewrote a file via a tool that bypassed the local
// kernel, miscounted inotify watches. Weekly is far cheaper than
// daily and still bounds worst-case divergence to ~7 days, which
// matches typical user-visible "huh, did that file ever sync?"
// detection latency.
const RescanBackstopReliable = 7 * 24 * time.Hour

// RescanBackstopUnreliable applies to filesystems classified as
// unreliable: NFS, SMB, FUSE-with-uncertain-event-semantics, etc.
// Daily because the safety-net is the *only* signal driving change
// detection on these mounts; there's no fsWatcher to fall back on.
const RescanBackstopUnreliable = 24 * time.Hour

// smartRescanAction decides whether a folder should be rescanned now
// based on watch-health signals (overflow, watch-limit-hit), wake
// events, filesystem trust, and the time since the last scheduled
// rescan. Returns nil when no action is due.
//
// Decision tree, in priority order:
//
//  1. Sleep/wake observed since last cycle → rescan now. The wake
//     event is global, so reconcile emits this for every folder in
//     one pass (one RescanFolderNow per folder is fine — Syncthing
//     handles a burst of scan requests).
//  2. Per-folder overflow flag set → rescan now. Highest-precision
//     signal: the kernel told us we missed events.
//  3. Watch-limit hit → rescan now. The folder couldn't be fully
//     watched; the next reconcile re-evaluates whether the limit
//     was raised.
//  4. Backstop interval exceeded for filesystem class → rescan now.
//     Catches the "kernel bug we don't know about yet" case.
//
// All paths leading to nil mean "no rescan due."
func smartRescanAction(obs FolderObs, folderPath string, observed Observed) Action {
	mk := func(reason string) Action {
		return RescanFolderNow{
			FolderID: obs.SyncthingFolderID,
			Path:     folderPath,
			Reason:   reason,
		}
	}

	if observed.SleepWakeSeen {
		return mk("suspend/resume detected")
	}

	health, hasHealth := observed.WatchHealthByPath[folderPath]

	if hasHealth {
		if health.OverflowSeen {
			return mk("event queue overflow")
		}
		if health.WatchLimitHit {
			return mk("watch limit reached")
		}
	}

	// Backstop: emit a rescan if we haven't rescanned in long enough.
	// Unknown filesystem classification → use the unreliable threshold
	// (safer default).
	backstop := RescanBackstopUnreliable
	if hasHealth && health.FilesystemReliable {
		backstop = RescanBackstopReliable
	}
	var lastRescan time.Time
	if observed.LastRescanByPath != nil {
		lastRescan = observed.LastRescanByPath[folderPath]
	}
	if observed.Now.Sub(lastRescan) >= backstop {
		if hasHealth && health.FilesystemReliable {
			return mk("weekly backstop")
		}
		return mk("daily backstop (untrusted filesystem)")
	}
	return nil
}

// IdleThresholdForPause is the duration a folder must be quiet on the
// local filesystem before the auto-pause logic pauses it. Aligned
// with the daily rescan interval — the design intent is "folders we
// haven't touched in a day stop costing CPU and memory." Per-folder
// overrides are a planned follow-up.
const IdleThresholdForPause = 24 * time.Hour

// RecentActivityForUnpause is the window within which observed
// activity on a paused folder triggers an immediate unpause. Short
// enough that legitimate user editing wakes the folder promptly;
// long enough that a brief unpause from background tooling doesn't
// thrash if the same folder goes quiet again within the window.
const RecentActivityForUnpause = 1 * time.Minute

// autoPauseAction returns the Pause/Unpause action the reconciler
// should emit for the given folder, or nil if no transition is due.
// Pure function — pulls all wall-clock information from `now` so
// Diff stays deterministic.
func autoPauseAction(obs FolderObs, folderPath string, activity map[string]time.Time, now time.Time) Action {
	last, ok := activity[folderPath]
	if !ok {
		// Tracker doesn't know this folder. Likely a transient state
		// (folder just added; tracker not yet populated). Skip
		// — the next reconcile after the tracker catches up will
		// reach the right decision.
		return nil
	}
	if obs.Paused {
		// Recent activity → unpause. Bounded "recent" window prevents
		// flapping when the tracker carries a stale-but-old timestamp
		// from before the pause.
		if now.Sub(last) <= RecentActivityForUnpause {
			return UnpauseSyncthingFolder{FolderID: obs.SyncthingFolderID}
		}
		return nil
	}
	// !Paused. Quiet long enough → pause.
	if now.Sub(last) >= IdleThresholdForPause {
		return PauseSyncthingFolder{FolderID: obs.SyncthingFolderID}
	}
	return nil
}

// folderScheduleDrifted reports whether the observed folder's
// scheduler fields differ from the canonical dotkeeper-managed values.
// Both fields are checked because a folder created by a third-party
// Syncthing UI might have one wrong and the other right; dotkeeper
// owns both.
//
// A RescanIntervalS of 0 is reserved by Syncthing for "watcher only,
// no periodic rescan" and is *not* the canonical value, so it triggers
// migration to 86400. If a user-facing per-folder override knob is
// added later, this check needs to consult the desired override rather
// than always reaching for the canonical default.
func folderScheduleDrifted(obs FolderObs) bool {
	if obs.RescanIntervalS != stclient.CanonicalRescanIntervalS {
		return true
	}
	if obs.FsWatcherEnabled != stclient.CanonicalFsWatcherEnabled {
		return true
	}
	return false
}

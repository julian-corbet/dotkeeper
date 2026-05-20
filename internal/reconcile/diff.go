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

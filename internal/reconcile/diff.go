// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
package reconcile

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"
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
// Repo reconciliation:
//   - Observed repo that IsDirty → GitCommitDirty with "auto: <short-path> <ISO timestamp>"
//   - Observed repo with a non-empty HeadCommit → GitPushRepo
//     (TODO: compare against StateV2.LastPushedCommit once schema-types lands)
func Diff(desired Desired, observed Observed) Plan {
	var plan Plan

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
			continue
		}
		// Check if device lists differ.
		if !stringSlicesEqual(sortedCopy(obs.Devices), sortedCopy(df.devices)) {
			plan = append(plan, UpdateSyncthingFolderDevices{
				FolderID: df.folderID,
				Devices:  df.devices,
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
		if repo.IsDirty {
			shortPath := filepath.Base(repo.Path)
			ts := time.Now().UTC().Format(time.RFC3339)
			plan = append(plan, GitCommitDirty{
				RepoPath: repo.Path,
				Message:  fmt.Sprintf("auto: %s %s", shortPath, ts),
			})
		}
		if repo.HeadCommit != "" {
			// TODO(v0.5/schema-types): Compare repo.HeadCommit against
			// StateV2.LastPushedCommit once schema-types is merged. For now we
			// emit GitPushRepo whenever there is any known HEAD commit, which is
			// idempotent because git push is a no-op when already up-to-date.
			plan = append(plan, GitPushRepo{RepoPath: repo.Path})
		}
	}

	return plan
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

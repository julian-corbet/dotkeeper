// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// SyncthingClient is the subset of the Syncthing REST API required by
// RealApplier. *stclient.Client satisfies this interface automatically.
type SyncthingClient interface {
	// GetConfig returns the full Syncthing configuration.
	GetConfig() (map[string]any, error)
	// SetConfig replaces the full Syncthing configuration.
	SetConfig(cfg map[string]any) error
	// GetStatus returns the system status (used to obtain the local device ID).
	GetStatus() (*stclient.SystemStatus, error)
	// AddOrUpdateFolder creates or merges a folder entry in the configuration.
	AddOrUpdateFolder(id, label, path string, deviceIDs []string) error
}

// RealApplier executes Actions against live system state. It is idempotent:
// applying the same Action twice produces the same observable outcome as
// applying it once.
type RealApplier struct {
	// ST is the Syncthing REST client used for folder management actions.
	// Inject *stclient.Client for production use; inject a test double in tests.
	ST SyncthingClient

	// Logger receives structured log events for each action applied.
	Logger *slog.Logger
}

// Apply dispatches to the correct handler based on the concrete Action type.
func (a *RealApplier) Apply(ctx context.Context, action Action) error {
	logger := a.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "applying action", "action", action.Describe())

	switch act := action.(type) {
	case AddSyncthingFolder:
		return a.applyAddSyncthingFolder(act)
	case RemoveSyncthingFolder:
		return a.applyRemoveSyncthingFolder(act)
	case UpdateSyncthingFolderDevices:
		return a.applyUpdateSyncthingFolderDevices(act)
	case GitCommitDirty:
		return applyGitCommitDirty(act)
	case GitPushRepo:
		return applyGitPushRepo(act)
	case TrackRepo:
		return applyTrackRepo(act)
	case UntrackRepo:
		return applyUntrackRepo(act)
	default:
		return fmt.Errorf("unknown action type: %T", action)
	}
}

// applyAddSyncthingFolder creates or updates the Syncthing folder configuration.
// Uses AddOrUpdateFolder which is idempotent — safe to call when the folder
// already exists with the same settings.
func (a *RealApplier) applyAddSyncthingFolder(act AddSyncthingFolder) error {
	// Use folder ID as label so the Syncthing UI shows something meaningful.
	if err := a.ST.AddOrUpdateFolder(act.FolderID, act.FolderID, act.Path, act.Devices); err != nil {
		return fmt.Errorf("AddSyncthingFolder %q: %w", act.FolderID, err)
	}
	return nil
}

// applyRemoveSyncthingFolder removes a folder from the Syncthing configuration.
// If the folder is not present the function is a no-op (idempotent).
func (a *RealApplier) applyRemoveSyncthingFolder(act RemoveSyncthingFolder) error {
	cfg, err := a.ST.GetConfig()
	if err != nil {
		return fmt.Errorf("RemoveSyncthingFolder %q: get config: %w", act.FolderID, err)
	}

	folders, _ := cfg["folders"].([]any)
	filtered := make([]any, 0, len(folders))
	for _, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] != act.FolderID {
			filtered = append(filtered, f)
		}
	}
	if len(filtered) == len(folders) {
		// Folder was not present — nothing to do.
		return nil
	}
	cfg["folders"] = filtered
	if err := a.ST.SetConfig(cfg); err != nil {
		return fmt.Errorf("RemoveSyncthingFolder %q: set config: %w", act.FolderID, err)
	}
	return nil
}

// applyUpdateSyncthingFolderDevices replaces the device list for an existing
// Syncthing folder. If the folder is not found the error is surfaced so the
// caller can decide how to handle the inconsistency.
func (a *RealApplier) applyUpdateSyncthingFolderDevices(act UpdateSyncthingFolderDevices) error {
	cfg, err := a.ST.GetConfig()
	if err != nil {
		return fmt.Errorf("UpdateSyncthingFolderDevices %q: get config: %w", act.FolderID, err)
	}

	// Fetch our own device ID so we can exclude it from the peer list.
	// Syncthing rejects a folder that lists the local device as a peer.
	status, err := a.ST.GetStatus()
	if err != nil {
		return fmt.Errorf("UpdateSyncthingFolderDevices %q: get status: %w", act.FolderID, err)
	}

	var folderDevices []map[string]any
	for _, did := range act.Devices {
		if did != status.MyID && did != "" {
			folderDevices = append(folderDevices, map[string]any{
				"deviceID":     did,
				"introducedBy": "",
			})
		}
	}

	folders, _ := cfg["folders"].([]any)
	found := false
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] == act.FolderID {
			fm["devices"] = folderDevices
			folders[i] = fm
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("UpdateSyncthingFolderDevices %q: folder not found in Syncthing config", act.FolderID)
	}

	cfg["folders"] = folders
	if err := a.ST.SetConfig(cfg); err != nil {
		return fmt.Errorf("UpdateSyncthingFolderDevices %q: set config: %w", act.FolderID, err)
	}
	return nil
}

// applyGitCommitDirty stages all changes and commits them with the supplied
// message. It respects GIT_AUTHOR_* and GIT_COMMITTER_* environment variables
// so the commit identity is controlled by the caller's environment.
// The function is idempotent: if there is nothing staged after `git add -A`
// it skips the commit.
func applyGitCommitDirty(act GitCommitDirty) error {
	if err := gitRun(act.RepoPath, "git", "add", "-A"); err != nil {
		return fmt.Errorf("GitCommitDirty %q: stage: %w", act.RepoPath, err)
	}

	// Check whether there is anything to commit.
	if gitRun(act.RepoPath, "git", "diff", "--cached", "--quiet") == nil {
		// Nothing staged — nothing to commit. Idempotent no-op.
		return nil
	}

	if err := gitRun(act.RepoPath, "git", "commit", "-m", act.Message); err != nil {
		return fmt.Errorf("GitCommitDirty %q: commit: %w", act.RepoPath, err)
	}
	return nil
}

// applyGitPushRepo pushes the current branch. The remote enforces
// fast-forward by default (receive.denyNonFastForwards); we do not pass
// --force so any divergence is surfaced as an error rather than silently
// overwritten. If the remote is already up-to-date the push succeeds
// silently (idempotent).
func applyGitPushRepo(act GitPushRepo) error {
	if err := gitRun(act.RepoPath, "git", "push"); err != nil {
		return fmt.Errorf("GitPushRepo %q: %w", act.RepoPath, err)
	}
	return nil
}

// applyTrackRepo appends the path to state.toml's tracked_overrides if not
// already present. Loads and writes the state file on every call; track/untrack
// are infrequent so the I/O cost is acceptable.
func applyTrackRepo(act TrackRepo) error {
	state, err := loadOrInitState()
	if err != nil {
		return fmt.Errorf("TrackRepo %q: load state: %w", act.Path, err)
	}

	for _, p := range state.TrackedOverrides {
		if p == act.Path {
			return nil // already tracked — idempotent no-op
		}
	}
	state.TrackedOverrides = append(state.TrackedOverrides, act.Path)

	if err := config.WriteStateV2(state); err != nil {
		return fmt.Errorf("TrackRepo %q: write state: %w", act.Path, err)
	}
	return nil
}

// applyUntrackRepo removes the path from state.toml's tracked_overrides.
// If the path is not present the function is a no-op (idempotent).
func applyUntrackRepo(act UntrackRepo) error {
	state, err := loadOrInitState()
	if err != nil {
		return fmt.Errorf("UntrackRepo %q: load state: %w", act.Path, err)
	}

	filtered := make([]string, 0, len(state.TrackedOverrides))
	for _, p := range state.TrackedOverrides {
		if p != act.Path {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == len(state.TrackedOverrides) {
		return nil // path was not present — idempotent no-op
	}
	state.TrackedOverrides = filtered

	if err := config.WriteStateV2(state); err != nil {
		return fmt.Errorf("UntrackRepo %q: write state: %w", act.Path, err)
	}
	return nil
}

// loadOrInitState loads state.toml, returning an empty StateV2 when the file
// does not yet exist.
func loadOrInitState() (*config.StateV2, error) {
	state, err := config.LoadStateV2()
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &config.StateV2{
			SchemaVersion:    2,
			TrackedOverrides: []string{},
			ObservedRepos:    make(map[string]config.ObservedRepo),
			LastSeenPeers:    make(map[string]time.Time),
		}
	}
	return state, nil
}

// gitRun executes a git command in the given directory, passing through the
// caller's environment so GIT_AUTHOR_* / GIT_COMMITTER_* identities are
// honoured. Returns a descriptive error that includes stderr output on failure.
func gitRun(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ() // propagate GIT_AUTHOR_* / GIT_COMMITTER_*
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

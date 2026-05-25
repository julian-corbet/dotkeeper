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
	"path/filepath"
	"strings"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/procnice"
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
	// AddDevice adds a Syncthing peer device if it is not already present.
	AddDevice(deviceID, name string) error
	// UpdateFolderSchedule rewrites the scheduler fields on an existing folder
	// to the canonical dotkeeper-managed values, used by the v0.9.5 drift
	// detector to migrate folders carried over from older installs.
	UpdateFolderSchedule(folderID string) error
	// SetFolderPaused toggles the folder's paused flag, used by the v0.9.6
	// auto-pause loop to suspend dormant folders and resume them on activity.
	SetFolderPaused(folderID string, paused bool) error
	// ScheduleRescan asks Syncthing to immediately rescan the named
	// folder, used by the v0.9.7 smart-rescan loop in response to
	// overflow/wake/backstop signals.
	ScheduleRescan(folderID string) error
}

// HealthResetter is called by the applier after a successful
// RescanFolderNow to clear the overflow / watch-limit flags on the
// watchhealth tracker. Optional — when nil, the applier just skips
// the reset and the tracker's flags persist until the next reset
// call from elsewhere (none today, so leaving HealthResetter nil
// would leak the flags). Wired by the daemon in cmd/dotkeeper.
type HealthResetter interface {
	Reset(path string)
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

	// Health, when non-nil, receives Reset(path) calls after a
	// RescanFolderNow action runs successfully. Lets the v0.9.7
	// smart-rescan loop clear its per-folder flags from inside the
	// applier without exposing the action.Path field to its caller.
	Health HealthResetter

	// LastRescanRecorder, when non-nil, is called after each
	// successful RescanFolderNow so reconcile can persist when the
	// scan was emitted. Used to drive the backstop interval. In-
	// memory map updated under a mutex; persisting to state.toml is
	// a v0.9.8 enhancement (current in-memory map resets on daemon
	// restart, which causes one extra rescan per folder per restart
	// — acceptable cost given restarts are rare).
	LastRescanRecorder LastRescanRecorder

	// Propagator, when non-nil, is invoked after every successful
	// GitCommitDirty to push the new commit to every paired peer
	// via the v1.0.0 multi-transport Manager. Best-effort: any
	// per-peer failure is logged but does not fail the action,
	// because the local commit is the system of record and peers
	// will catch up on the next reconcile cycle (or via Syncthing's
	// universal fallback).
	Propagator CommitPropagator
}

// CommitPropagator is the minimum surface RealApplier needs from the
// transport-manager wiring at the daemon level. Kept as an
// interface so the reconcile package stays independent of
// internal/transport's concrete types (which import Folder/Peer/
// Change values that would otherwise leak into reconcile).
//
// Implementation lives in cmd/dotkeeper/main.go where it has the
// real Manager + peer roster from machine.toml.
type CommitPropagator interface {
	// PropagateNewCommit is called once per successful auto-commit.
	// folderPath identifies the folder (its absolute working-tree
	// path on this host); the implementation resolves the
	// dotkeeper folder ID from that path and fans out to every
	// peer in the daemon's roster, picks a transport via
	// Manager.Route, executes the push, and feeds observed
	// elapsed back to Manager.RecordTransfer.
	PropagateNewCommit(ctx context.Context, folderPath string)
}

// LastRescanRecorder records the timestamp at which dotkeeper asked
// Syncthing to rescan the folder. Pulled out as an interface so the
// daemon can supply a real in-memory implementation and tests can
// supply a no-op.
type LastRescanRecorder interface {
	RecordRescan(path string, at time.Time)
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
	case UpdateSyncthingFolderSchedule:
		return a.applyUpdateSyncthingFolderSchedule(act)
	case PauseSyncthingFolder:
		return a.applyPauseSyncthingFolder(act)
	case UnpauseSyncthingFolder:
		return a.applyUnpauseSyncthingFolder(act)
	case RescanFolderNow:
		return a.applyRescanFolderNow(act)
	case AddSyncthingDevice:
		return a.applyAddSyncthingDevice(act)
	case EnsureIgnoreFile:
		return applyEnsureIgnoreFile(act)
	case EnsureFolderMarker:
		return applyEnsureFolderMarker(act)
	case AcceptSubscription:
		return a.applyAcceptSubscription(act)
	case GitCommitDirty:
		// Capture HEAD before the commit attempt. applyGitCommitDirty
		// may take a fast path that returns successfully without
		// creating a new commit (e.g., `git add -A` produced no
		// staged changes because the dirty state was undone between
		// observe and apply). We only want to invoke the propagator
		// when an ACTUAL new commit landed — otherwise the
		// daemon-side daemonPropagator would compute an estimated
		// transfer size from a stale HEAD~1..HEAD diff and feed
		// the cost model bogus observations.
		preHead := gitHeadHash(act.RepoPath)
		if err := applyGitCommitDirty(act); err != nil {
			return err
		}
		postHead := gitHeadHash(act.RepoPath)
		if a.Propagator != nil && preHead != postHead {
			// New commit exists; fan out to peers via the v1.0.0
			// transport manager. Best-effort: per-peer push
			// failures are logged but don't fail the action,
			// because the local commit is canonical state and
			// peers eventually catch up via Syncthing's universal
			// fallback or the next reconcile cycle.
			a.Propagator.PropagateNewCommit(ctx, act.RepoPath)
		}
		return nil
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

// applyAcceptSubscription provisions a folder declared by a
// subscription:
//
//  1. Resolve the local path (already absolute and expanded by
//     Diff; defensive expansion here covers paths typed by hand
//     into state.toml).
//  2. mkdir -p — Syncthing requires the folder root to exist
//     before adding it.
//  3. Write a minimal .dotkeeper.toml so the next discovery scan
//     picks the folder up via the normal path. Without this, the
//     diff-vs-desired loop would see the folder as
//     "observed but not desired" on the next reconcile and emit
//     RemoveSyncthingFolder.
//  4. Add the folder to Syncthing's config with the offering peer
//     in share-with. From here, BEP gossip from the offerer seeds
//     the working tree.
//
// Auto-clone via `git clone CloneRemote Path` is intentionally
// deferred — Syncthing will populate the path from the peer's
// existing working tree, which is enough for content. Operators
// who want git history can run `git init && git remote add origin
// <Label>` themselves; a follow-up PR will automate this when
// CloneRemote is set.
func (a *RealApplier) applyAcceptSubscription(act AcceptSubscription) error {
	if a.ST == nil {
		return fmt.Errorf("AcceptSubscription %q: Syncthing client not available", act.Label)
	}
	if act.Path == "" {
		return fmt.Errorf("AcceptSubscription %q: empty path", act.Label)
	}
	path := expandSubscriptionPath(act.Path)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("AcceptSubscription %q: mkdir %q: %w", act.Label, path, err)
	}
	if err := writeSubscriptionMarker(path, act); err != nil {
		return fmt.Errorf("AcceptSubscription %q: write .dotkeeper.toml: %w", act.Label, err)
	}
	if err := applyEnsureFolderMarker(EnsureFolderMarker{RepoPath: path}); err != nil {
		return fmt.Errorf("AcceptSubscription %q: ensure marker: %w", act.Label, err)
	}
	if err := a.ST.AddOrUpdateFolder(act.FolderID, act.Label, path, []string{act.FromDeviceID}); err != nil {
		return fmt.Errorf("AcceptSubscription %q: AddOrUpdateFolder: %w", act.Label, err)
	}
	return nil
}

// expandSubscriptionPath normalises a path that may have come from
// hand-edited TOML: leading ~ → home dir, relative → absolute.
func expandSubscriptionPath(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	return p
}

// writeSubscriptionMarker creates the minimal .dotkeeper.toml that
// makes the just-provisioned folder visible to the discovery scan.
// The file is written via the public config writer so future
// schema bumps don't require a parallel marker-writer here.
func writeSubscriptionMarker(path string, act AcceptSubscription) error {
	// If the file already exists, don't overwrite — operator may
	// have customised it. Re-tracking via discovery will pick up
	// their version.
	markerPath := filepath.Join(path, config.RepoConfigFileName)
	if _, err := os.Stat(markerPath); err == nil {
		return nil
	}
	cfg := &config.RepoConfigV2{
		SchemaVersion: 2,
		Meta: config.RepoMeta{
			Name:    filepath.Base(path),
			Added:   time.Now().UTC().Format(time.RFC3339),
			AddedBy: "subscription:" + act.FromPeerName,
		},
		Sync: config.RepoSyncConfig{
			SyncthingFolderID: act.FolderID,
			Ignore:            []string{},
			ShareWith:         []string{act.FromDeviceID},
		},
		Commit:    config.RepoCommitConfig{},
		GitBackup: config.RepoGitBackupConfig{SkipSlots: []uint{}},
	}
	// If the label looks like a canonical git-remote URL, populate
	// [git] so the next-tick discovery can use it for routing.
	if strings.Contains(act.Label, "/") && !strings.HasPrefix(act.Label, "dk:") {
		cfg.Git = config.RepoGitConfig{Canonical: act.Label}
	}
	return config.WriteRepoConfigV2(path, cfg)
}

func applyEnsureFolderMarker(act EnsureFolderMarker) error {
	if act.RepoPath == "" {
		return fmt.Errorf("EnsureFolderMarker: empty repo path")
	}
	if info, err := os.Stat(act.RepoPath); err != nil {
		return fmt.Errorf("EnsureFolderMarker %q: stat repo: %w", act.RepoPath, err)
	} else if !info.IsDir() {
		return fmt.Errorf("EnsureFolderMarker %q: not a directory", act.RepoPath)
	}

	markerPath := filepath.Join(act.RepoPath, stclient.FolderMarkerName)
	if info, err := os.Stat(markerPath); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("EnsureFolderMarker %q: marker exists but is not a directory", act.RepoPath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("EnsureFolderMarker %q: stat marker: %w", act.RepoPath, err)
	}
	if err := os.Mkdir(markerPath, 0o755); err != nil {
		return fmt.Errorf("EnsureFolderMarker %q: create marker: %w", act.RepoPath, err)
	}
	return nil
}

func applyEnsureIgnoreFile(act EnsureIgnoreFile) error {
	if act.RepoPath == "" {
		return fmt.Errorf("EnsureIgnoreFile: empty repo path")
	}
	if info, err := os.Stat(act.RepoPath); err != nil {
		return fmt.Errorf("EnsureIgnoreFile %q: stat repo: %w", act.RepoPath, err)
	} else if !info.IsDir() {
		return fmt.Errorf("EnsureIgnoreFile %q: not a directory", act.RepoPath)
	}

	want := config.SyncIgnoreFileContent(act.Patterns)
	path := filepath.Join(act.RepoPath, ".stignore")
	if data, err := os.ReadFile(path); err == nil && string(data) == want {
		return ensureGitInfoExclude(act.RepoPath, config.DefaultGitExcludePatterns...)
	}
	if err := config.WriteFileAtomic(path, []byte(want), 0o644); err != nil {
		return fmt.Errorf("EnsureIgnoreFile %q: write .stignore: %w", act.RepoPath, err)
	}
	if err := ensureGitInfoExclude(act.RepoPath, config.DefaultGitExcludePatterns...); err != nil {
		return fmt.Errorf("EnsureIgnoreFile %q: update git exclude: %w", act.RepoPath, err)
	}
	return nil
}

func ensureGitInfoExclude(repoPath string, patterns ...string) error {
	path, ok := gitInfoExcludePath(repoPath)
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)
	var missing []string
	for _, pattern := range patterns {
		if !excludeHasPattern(content, pattern) {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	if !strings.Contains(content, "# dotkeeper local files") {
		b.WriteString("# dotkeeper local files\n")
	}
	for _, pattern := range missing {
		b.WriteString(pattern)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.WriteFileAtomic(path, []byte(b.String()), 0o644)
}

func gitInfoExcludePath(repoPath string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = repoPath
	out, err := procnice.Output(cmd)
	if err != nil {
		return "", false
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, path)
	}
	return path, true
}

func excludeHasPattern(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

func (a *RealApplier) applyAddSyncthingDevice(act AddSyncthingDevice) error {
	if a.ST == nil {
		return fmt.Errorf("AddSyncthingDevice %q: Syncthing client not available (is Syncthing running?)", act.Name)
	}
	if err := a.ST.AddDevice(act.DeviceID, act.Name); err != nil {
		return fmt.Errorf("AddSyncthingDevice %q: %w", act.Name, err)
	}
	return nil
}

// applyAddSyncthingFolder creates or updates the Syncthing folder configuration.
// Uses AddOrUpdateFolder which is idempotent — safe to call when the folder
// already exists with the same settings.
func (a *RealApplier) applyAddSyncthingFolder(act AddSyncthingFolder) error {
	if a.ST == nil {
		return fmt.Errorf("AddSyncthingFolder %q: Syncthing client not available (is Syncthing running?)", act.FolderID)
	}
	if err := applyEnsureFolderMarker(EnsureFolderMarker{RepoPath: act.Path}); err != nil {
		return fmt.Errorf("AddSyncthingFolder %q: %w", act.FolderID, err)
	}
	// Prefer the git-canonical label when available; fall back to
	// folder ID for non-git folders and pre-Phase-2 installs that
	// don't yet carry an identity. The canonical label is what the
	// subscription matcher reads from peer ClusterConfigs.
	label := act.Label
	if label == "" {
		label = act.FolderID
	}
	if err := a.ST.AddOrUpdateFolder(act.FolderID, label, act.Path, act.Devices); err != nil {
		return fmt.Errorf("AddSyncthingFolder %q: %w", act.FolderID, err)
	}
	return nil
}

// applyRemoveSyncthingFolder removes a folder from the Syncthing configuration.
// If the folder is not present the function is a no-op (idempotent).
func (a *RealApplier) applyRemoveSyncthingFolder(act RemoveSyncthingFolder) error {
	if a.ST == nil {
		return fmt.Errorf("RemoveSyncthingFolder %q: Syncthing client not available (is Syncthing running?)", act.FolderID)
	}
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
	if a.ST == nil {
		return fmt.Errorf("UpdateSyncthingFolderDevices %q: Syncthing client not available (is Syncthing running?)", act.FolderID)
	}
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
//
// Deletion safeguard: dotkeeper auto-commit must never propagate accidental
// deletions caused by plain `rm`, filesystem faults, or sync glitches. We
// capture deletions the user has explicitly staged via `git rm` before
// staging, then unstage any new deletions that `git add -A` produced.
//
// Local-only debug note: after a successful commit, a git note in
// refs/notes/dotkeeper records the machine name and time. Notes are not
// pushed by default, so commits going to a remote — including public
// remotes — never carry machine-identifying metadata.
func applyGitCommitDirty(act GitCommitDirty) error {
	preDels := stagedDeletionsApplier(act.RepoPath)

	if err := gitRun(act.RepoPath, "git", "add", "-A"); err != nil {
		return fmt.Errorf("GitCommitDirty %q: stage: %w", act.RepoPath, err)
	}

	// Fast path: if `git add -A` produced an empty staged set, there is by
	// definition nothing to commit and no unintended deletion to reset.
	// Skip the second stagedDeletionsApplier call — saves one `git diff` per
	// "dirty-but-no-real-changes" repo (e.g. touch-only timestamp churn).
	if gitRun(act.RepoPath, "git", "diff", "--cached", "--quiet") == nil {
		return nil
	}

	preDelSet := make(map[string]bool, len(preDels))
	for _, f := range preDels {
		preDelSet[f] = true
	}
	var unintended []string
	for _, f := range stagedDeletionsApplier(act.RepoPath) {
		if !preDelSet[f] {
			unintended = append(unintended, f)
		}
	}
	if len(unintended) > 0 {
		args := append([]string{"reset", "HEAD", "--"}, unintended...)
		_ = gitRun(act.RepoPath, "git", args...)
		// Resetting unintended deletions may leave the index empty; bail
		// before invoking `git commit` on a no-op staged set.
		if gitRun(act.RepoPath, "git", "diff", "--cached", "--quiet") == nil {
			return nil
		}
	}

	if err := gitRun(act.RepoPath, "git", "commit", "-m", act.Message); err != nil {
		return fmt.Errorf("GitCommitDirty %q: commit: %w", act.RepoPath, err)
	}

	if cfg, err := config.LoadMachineConfigV2(); err == nil && cfg != nil {
		note := fmt.Sprintf("machine=%s\ntime=%s\n", cfg.Name, time.Now().UTC().Format(time.RFC3339))
		_ = gitRun(act.RepoPath, "git", "notes", "--ref=dotkeeper", "add", "-f", "-m", note, "HEAD")
	}

	return nil
}

// gitHeadHash returns the current HEAD commit hash for the repo at
// repoPath, or the empty string when HEAD can't be resolved (no
// commits yet, repo missing, permission denied, …). Used to detect
// whether applyGitCommitDirty actually produced a new commit: pre
// vs post comparison.
//
// Returns "" rather than an error because the only consumer
// (GitCommitDirty handling) treats both pre and post equally and
// only branches on equality. A read failure that returns "" on
// both sides correctly resolves to "no new commit," which matches
// the safe behaviour (skip the propagator).
func gitHeadHash(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := procnice.Output(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// stagedDeletionsApplier returns paths currently staged for deletion in the
// given repo. A nil return indicates either no deletions or a git failure;
// both are safe to treat as "no pre-existing deletions" by callers.
func stagedDeletionsApplier(repoPath string) []string {
	cmd := exec.Command("git", "diff", "--cached", "--name-only", "--diff-filter=D")
	cmd.Dir = repoPath
	out, err := procnice.Output(cmd)
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// applyGitPushRepo pushes the current branch. The remote enforces
// fast-forward by default (receive.denyNonFastForwards); we do not pass
// --force so any divergence is surfaced as an error rather than silently
// overwritten. If the remote is already up-to-date the push succeeds
// silently (idempotent).
func applyGitPushRepo(act GitPushRepo) error {
	if err := gitRun(act.RepoPath, "git", "pull", "--rebase", "--autostash", "--quiet"); err != nil {
		_ = gitRun(act.RepoPath, "git", "rebase", "--abort")
		return fmt.Errorf("GitPushRepo %q: pull before push: %w", act.RepoPath, err)
	}
	if err := gitRun(act.RepoPath, "git", "push"); err != nil {
		return fmt.Errorf("GitPushRepo %q: %w", act.RepoPath, err)
	}
	if err := markRepoPushed(act.RepoPath); err != nil {
		return fmt.Errorf("GitPushRepo %q: record state: %w", act.RepoPath, err)
	}
	return nil
}

func markRepoPushed(repoPath string) error {
	head := gitHeadCommit(repoPath)
	if head == "" {
		return nil
	}
	return config.MutateStateV2(func(state *config.StateV2) error {
		if state.ObservedRepos == nil {
			state.ObservedRepos = make(map[string]config.ObservedRepo)
		}
		state.ObservedRepos[repoPath] = config.ObservedRepo{
			LastReconciledCommit: head,
			LastPushedCommit:     head,
			LastBackupAt:         time.Now().UTC(),
		}
		return nil
	})
}

// applyTrackRepo appends the path to state.toml's tracked_overrides if not
// already present. The read-modify-write cycle runs under MutateStateV2's
// exclusive flock so concurrent `dotkeeper track` invocations cannot lose
// each other's updates or corrupt the file.
func applyTrackRepo(act TrackRepo) error {
	if err := config.MutateStateV2(func(state *config.StateV2) error {
		for _, p := range state.TrackedOverrides {
			if p == act.Path {
				return nil // already tracked — idempotent no-op
			}
		}
		state.TrackedOverrides = append(state.TrackedOverrides, act.Path)
		return nil
	}); err != nil {
		return fmt.Errorf("TrackRepo %q: %w", act.Path, err)
	}
	return nil
}

// applyUntrackRepo removes the path from state.toml's tracked_overrides.
// If the path is not present the function is a no-op (idempotent).
func applyUntrackRepo(act UntrackRepo) error {
	if err := config.MutateStateV2(func(state *config.StateV2) error {
		filtered := make([]string, 0, len(state.TrackedOverrides))
		for _, p := range state.TrackedOverrides {
			if p != act.Path {
				filtered = append(filtered, p)
			}
		}
		state.TrackedOverrides = filtered
		return nil
	}); err != nil {
		return fmt.Errorf("UntrackRepo %q: %w", act.Path, err)
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

// applyUpdateSyncthingFolderSchedule rewrites the scheduler fields on
// an existing folder. Driving case is migrating folders left at
// rescanIntervalS=60 by v0.9.3-and-earlier installs.
func (a *RealApplier) applyUpdateSyncthingFolderSchedule(act UpdateSyncthingFolderSchedule) error {
	if a.ST == nil {
		return fmt.Errorf("UpdateSyncthingFolderSchedule %q: Syncthing client not available (is Syncthing running?)", act.FolderID)
	}
	if err := a.ST.UpdateFolderSchedule(act.FolderID); err != nil {
		return fmt.Errorf("UpdateSyncthingFolderSchedule %q: %w", act.FolderID, err)
	}
	return nil
}

func (a *RealApplier) applyPauseSyncthingFolder(act PauseSyncthingFolder) error {
	if a.ST == nil {
		return fmt.Errorf("PauseSyncthingFolder %q: Syncthing client not available", act.FolderID)
	}
	if err := a.ST.SetFolderPaused(act.FolderID, true); err != nil {
		return fmt.Errorf("PauseSyncthingFolder %q: %w", act.FolderID, err)
	}
	return nil
}

func (a *RealApplier) applyUnpauseSyncthingFolder(act UnpauseSyncthingFolder) error {
	if a.ST == nil {
		return fmt.Errorf("UnpauseSyncthingFolder %q: Syncthing client not available", act.FolderID)
	}
	if err := a.ST.SetFolderPaused(act.FolderID, false); err != nil {
		return fmt.Errorf("UnpauseSyncthingFolder %q: %w", act.FolderID, err)
	}
	return nil
}

func (a *RealApplier) applyRescanFolderNow(act RescanFolderNow) error {
	if a.ST == nil {
		return fmt.Errorf("RescanFolderNow %q: Syncthing client not available", act.FolderID)
	}
	if err := a.ST.ScheduleRescan(act.FolderID); err != nil {
		return fmt.Errorf("RescanFolderNow %q: %w", act.FolderID, err)
	}
	// Reset watchhealth flags only on success — if the rescan failed
	// we want the flags to stay raised so the next reconcile cycle
	// retries.
	if a.Health != nil && act.Path != "" {
		a.Health.Reset(act.Path)
	}
	if a.LastRescanRecorder != nil && act.Path != "" {
		a.LastRescanRecorder.RecordRescan(act.Path, time.Now())
	}
	return nil
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

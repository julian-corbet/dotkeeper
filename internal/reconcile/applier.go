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
	case AddSyncthingDevice:
		return a.applyAddSyncthingDevice(act)
	case EnsureIgnoreFile:
		return applyEnsureIgnoreFile(act)
	case EnsureFolderMarker:
		return applyEnsureFolderMarker(act)
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
	// Use folder ID as label so the Syncthing UI shows something meaningful.
	if err := a.ST.AddOrUpdateFolder(act.FolderID, act.FolderID, act.Path, act.Devices); err != nil {
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

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
//
// This file provides factory functions that produce DesiredProvider and
// ObservedProvider closures wired to real on-disk config and live system
// state.  The factories accept explicit paths so callers (and tests) can
// redirect reads without touching XDG environment variables.
package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// NewDesiredProvider returns a DesiredProvider that reads machine.toml from
// machineConfigPath, walks each declared scan root for dotkeeper.toml files,
// loads state.toml from stateConfigPath for the peer roster, and assembles a
// Desired via BuildDesired.
//
// If machineConfigPath does not exist the returned provider yields an error
// that points the user at "dotkeeper init". A missing state file is not an
// error — peers will simply be empty until the user pairs with a peer
// (correct first-run behaviour).
func NewDesiredProvider(machineConfigPath, stateConfigPath string) DesiredProvider {
	return func(_ context.Context) (Desired, error) {
		machine, err := loadMachineConfigFromPath(machineConfigPath)
		if err != nil {
			return Desired{}, err
		}

		repos, err := discoverRepos(machine)
		if err != nil {
			return Desired{}, fmt.Errorf("repo discovery failed: %w", err)
		}

		// State is optional only in the missing-file case (first-run, before
		// any peers have been paired). A malformed or unreadable state file
		// must be a hard error: silently ignoring it would yield an empty
		// peer roster and the reconciler would then plan to *remove* every
		// known Syncthing peer. Distinguish "absent" (safe) from "broken"
		// (catastrophic) here.
		state, _, err := loadStateFromPath(stateConfigPath)
		if err != nil {
			return Desired{}, fmt.Errorf("loading state from %s: %w", stateConfigPath, err)
		}

		return BuildDesired(machine, repos, state), nil
	}
}

// loadMachineConfigFromPath reads machine.toml from path and applies defaults.
// Returns a clear error (pointing at dotkeeper init) when the file is absent.
func loadMachineConfigFromPath(path string) (*config.MachineConfigV2, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf(
			"machine config not found at %s — run \"dotkeeper init\" to create it",
			path,
		)
	}

	var cfg config.MachineConfigV2
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading machine config from %s: %w", path, err)
	}

	// Reuse the same defaults logic by writing into a scratch value and
	// mirroring the behaviour of config.LoadMachineConfigV2.
	applyMachineV2DefaultsLocal(&cfg)
	return &cfg, nil
}

// applyMachineV2DefaultsLocal mirrors config.applyMachineV2Defaults for use
// outside the config package (which does not export that function).
func applyMachineV2DefaultsLocal(cfg *config.MachineConfigV2) {
	if cfg.DefaultCommitPolicy == "" {
		cfg.DefaultCommitPolicy = "manual"
	}
	if cfg.DefaultGitInterval == "" {
		cfg.DefaultGitInterval = "hourly"
	}
	if cfg.DefaultSlotOffsetMinutes == 0 {
		cfg.DefaultSlotOffsetMinutes = 5
	}
	if len(cfg.Discovery.ScanRoots) == 0 {
		cfg.Discovery.ScanRoots = []string{"~/Documents/GitHub", "~/.agent"}
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
	if cfg.Discovery.Exclude == nil {
		cfg.Discovery.Exclude = []string{}
	}
}

// discoverRepos walks each scan root declared in machine.Discovery and returns
// a map of absolute repo path → RepoConfigV2 for every directory that contains
// a dotkeeper.toml file within the configured depth.
func discoverRepos(machine *config.MachineConfigV2) (map[string]*config.RepoConfigV2, error) {
	repos := make(map[string]*config.RepoConfigV2)

	excludeSet := make(map[string]struct{}, len(machine.Discovery.Exclude))
	for _, ex := range machine.Discovery.Exclude {
		abs, err := expandTilde(ex)
		if err != nil {
			continue
		}
		excludeSet[abs] = struct{}{}
	}

	depth := machine.Discovery.ScanDepth
	if depth <= 0 {
		depth = 3
	}

	for _, root := range machine.Discovery.ScanRoots {
		absRoot, err := expandTilde(root)
		if err != nil {
			// Skip unresolvable roots rather than aborting the whole scan.
			continue
		}

		if err := walkScanRoot(absRoot, excludeSet, depth, repos); err != nil {
			return nil, fmt.Errorf("walking scan root %s: %w", absRoot, err)
		}
	}

	return repos, nil
}

// walkScanRoot recursively walks dir up to maxDepth levels deep and adds any
// directory containing a dotkeeper.toml to repos.
func walkScanRoot(
	dir string,
	excludeSet map[string]struct{},
	maxDepth int,
	repos map[string]*config.RepoConfigV2,
) error {
	return walkDir(dir, excludeSet, 0, maxDepth, repos)
}

// walkDir is the recursive core of walkScanRoot.
func walkDir(
	dir string,
	excludeSet map[string]struct{},
	currentDepth, maxDepth int,
	repos map[string]*config.RepoConfigV2,
) error {
	if _, excluded := excludeSet[dir]; excluded {
		return nil
	}

	// Check if dotkeeper.toml exists at this level.
	markerPath := filepath.Join(dir, "dotkeeper.toml")
	if _, err := os.Stat(markerPath); err == nil {
		repoCfg, err := config.LoadRepoConfigV2(dir)
		if err != nil {
			// Non-fatal: record the path with a nil config so the caller can
			// see the repo was discovered even if its config is malformed.
			repos[dir] = nil
		} else {
			repos[dir] = repoCfg
		}
		// Per ADR 0004, a repo directory is a leaf for discovery: we don't
		// descend into sub-directories of a managed repo.
		return nil
	}

	if currentDepth >= maxDepth {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable directory — skip silently (e.g. permission-denied).
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if err := walkDir(child, excludeSet, currentDepth+1, maxDepth, repos); err != nil {
			return err
		}
	}
	return nil
}

// expandTilde expands a leading "~" to the current user's home directory.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, path[1:]), nil
}

// -------------------------------------------------------------------
// ObservedProvider
// -------------------------------------------------------------------

// SyncthingQuerier is the subset of stclient.Client used by NewObservedProvider.
// Defining a narrow interface here lets tests inject a stub without touching the
// production client.
type SyncthingQuerier interface {
	GetConfig() (map[string]any, error)
	GetConnections() (*stclient.Connections, error)
}

// NewObservedProvider returns an ObservedProvider that queries the live
// Syncthing instance (via stClient) and reads state.toml from stateConfigPath.
//
// If stClient is nil or Syncthing is unreachable the returned Observed has
// empty ManagedFolders/LivePeers slices; the error is wrapped with context.
func NewObservedProvider(stClient *stclient.Client, stateConfigPath string) ObservedProvider {
	return newObservedProvider(stClient, stateConfigPath)
}

// newObservedProvider accepts the SyncthingQuerier interface directly, which
// lets tests pass a stub without wrapping the real stclient.Client.
func newObservedProvider(querier SyncthingQuerier, stateConfigPath string) ObservedProvider {
	return func(_ context.Context) (Observed, error) {
		obs := Observed{}

		// 1. Live Syncthing folder config.
		if querier != nil {
			folders, peers, stErr := querySyncthing(querier)
			if stErr != nil {
				// Non-fatal: return partial Observed so callers can still diff
				// the git state even when Syncthing is momentarily down.
				return obs, fmt.Errorf("querying Syncthing: %w", stErr)
			}
			obs.ManagedFolders = folders
			obs.LivePeers = peers
		}

		// 2. state.toml for cached state + tracked override paths.
		cachedState, trackedPaths, err := loadStateFromPath(stateConfigPath)
		if err != nil {
			return obs, fmt.Errorf("loading state from %s: %w", stateConfigPath, err)
		}
		obs.CachedState = cachedState

		// 3. Per-repo git state for all tracked override paths.
		for _, repoPath := range trackedPaths {
			repoObs := queryRepoGitState(repoPath, cachedState)
			obs.TrackedRepos = append(obs.TrackedRepos, repoObs)
		}

		return obs, nil
	}
}

// querySyncthing fetches live folder and peer state from a SyncthingQuerier.
func querySyncthing(q SyncthingQuerier) ([]FolderObs, []LivePeer, error) {
	cfg, err := q.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("GetConfig: %w", err)
	}

	conns, err := q.GetConnections()
	if err != nil {
		return nil, nil, fmt.Errorf("GetConnections: %w", err)
	}

	// Parse folders from the raw config map.
	var folders []FolderObs
	if rawFolders, ok := cfg["folders"].([]any); ok {
		for _, rf := range rawFolders {
			fm, ok := rf.(map[string]any)
			if !ok {
				continue
			}
			folderID, _ := fm["id"].(string)
			folderPath, _ := fm["path"].(string)
			if folderID == "" {
				continue
			}

			var devices []string
			if rawDevices, ok := fm["devices"].([]any); ok {
				for _, rd := range rawDevices {
					dm, ok := rd.(map[string]any)
					if !ok {
						continue
					}
					if did, ok := dm["deviceID"].(string); ok && did != "" {
						devices = append(devices, did)
					}
				}
			}

			folders = append(folders, FolderObs{
				SyncthingFolderID: folderID,
				Path:              folderPath,
				Devices:           devices,
			})
		}
	}

	// Build LivePeer slice from connections map.
	var peers []LivePeer
	if conns != nil {
		for deviceID, conn := range conns.Connections {
			peers = append(peers, LivePeer{
				DeviceID:  deviceID,
				LastSeen:  time.Now(), // Syncthing REST does not surface last-seen in connections
				Connected: conn.Connected,
			})
		}
	}

	return folders, peers, nil
}

// loadStateFromPath reads state.toml from path. Returns (nil, nil, nil) when
// the file does not exist (first-run case). Returns the tracked override paths
// alongside the StateV2 so the caller does not have to nil-check cachedState.
func loadStateFromPath(path string) (*config.StateV2, []string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil, nil
	}

	var s config.StateV2
	if _, err := toml.DecodeFile(path, &s); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", path, err)
	}

	// Mirror the nil-safety logic from config.LoadStateV2.
	if s.Peers == nil {
		s.Peers = []config.PeerEntry{}
	}
	if s.TrackedOverrides == nil {
		s.TrackedOverrides = []string{}
	}
	if s.ObservedRepos == nil {
		s.ObservedRepos = make(map[string]config.ObservedRepo)
	}
	if s.LastSeenPeers == nil {
		s.LastSeenPeers = make(map[string]time.Time)
	}

	return &s, s.TrackedOverrides, nil
}

// queryRepoGitState returns a RepoObs for the given repo path by invoking git
// directly.  Errors from git (e.g. not a git repo) result in zero values for
// HeadCommit and IsDirty rather than a hard failure — the reconciler should
// still see the repo and can emit appropriate actions.
func queryRepoGitState(repoPath string, state *config.StateV2) RepoObs {
	obs := RepoObs{Path: repoPath}

	obs.HeadCommit = gitHeadCommit(repoPath)
	obs.IsDirty = gitIsDirty(repoPath)

	if state != nil {
		if sr, ok := state.ObservedRepos[repoPath]; ok {
			obs.LastBackupAt = sr.LastBackupAt
		}
	}

	return obs
}

// gitHeadCommit returns the current HEAD commit hash, or empty string on error.
func gitHeadCommit(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// gitIsDirty reports whether the working tree has uncommitted changes.
// Returns false on error (treats unknown repos as clean to avoid false positives).
func gitIsDirty(repoPath string) bool {
	// "git status --porcelain" exits 0 always; non-empty stdout means dirty.
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(out.String()) != ""
}

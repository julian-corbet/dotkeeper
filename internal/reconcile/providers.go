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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/procnice"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// NewDesiredProvider returns a DesiredProvider that reads machine.toml from
// machineConfigPath, walks each declared scan root for .dotkeeper.toml files,
// loads state.toml from stateConfigPath for the peer roster, and assembles a
// Desired via BuildDesired.
//
// If machineConfigPath does not exist the returned provider yields an error
// that points the user at "dotkeeper init". A missing state file is not an
// error — peers will simply be empty until the user pairs with a peer
// (correct first-run behaviour).
func NewDesiredProvider(machineConfigPath, stateConfigPath string) DesiredProvider {
	cache := &configCache{}
	return func(_ context.Context) (Desired, error) {
		machine, err := cache.loadMachine(machineConfigPath)
		if err != nil {
			return Desired{}, err
		}

		repos, err := discoverRepos(machine, cache.loadRepo)
		if err != nil {
			return Desired{}, fmt.Errorf("repo discovery failed: %w", err)
		}

		// State is optional only in the missing-file case (first-run, before
		// any peers have been paired). A malformed or unreadable state file
		// must be a hard error: silently ignoring it would yield an empty
		// peer roster and the reconciler would then plan to *remove* every
		// known Syncthing peer. Distinguish "absent" (safe) from "broken"
		// (catastrophic) here.
		state, trackedPaths, err := cache.loadState(stateConfigPath)
		if err != nil {
			return Desired{}, fmt.Errorf("loading state from %s: %w", stateConfigPath, err)
		}
		if err := mergeTrackedOverrideRepos(repos, trackedPaths, cache.loadRepo); err != nil {
			return Desired{}, err
		}

		return BuildDesired(machine, repos, state), nil
	}
}

func mergeTrackedOverrideRepos(repos map[string]*config.RepoConfigV2, trackedPaths []string, loadRepo repoLoader) error {
	if loadRepo == nil {
		loadRepo = defaultRepoLoader
	}
	for _, rawPath := range trackedPaths {
		expanded, err := expandTilde(rawPath)
		if err != nil {
			return fmt.Errorf("resolving tracked repo path %q: %w", rawPath, err)
		}
		absPath, err := filepath.Abs(expanded)
		if err != nil {
			return fmt.Errorf("resolving tracked repo path %q: %w", rawPath, err)
		}
		if _, exists := repos[absPath]; exists {
			continue
		}
		repoCfg, err := loadRepo(absPath)
		if err != nil {
			return fmt.Errorf("loading tracked repo config %s: %w", absPath, err)
		}
		if repoCfg == nil {
			continue
		}
		repos[absPath] = repoCfg
	}
	return nil
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
		cfg.Peers = []config.PeerEntry{}
	}
	if cfg.Discovery.Exclude == nil {
		cfg.Discovery.Exclude = []string{}
	}
}

// repoLoader is the per-directory loader used during a scan. A nil loader
// means "use config.LoadRepoConfigV2 directly"; production code passes the
// mtime-aware cache from configCache.loadRepo so repeated reconciles of
// unchanged repos pay no TOML-parse cost.
type repoLoader func(repoDir string) (*config.RepoConfigV2, error)

func defaultRepoLoader(repoDir string) (*config.RepoConfigV2, error) {
	return config.LoadRepoConfigV2(repoDir)
}

// discoverRepos walks each scan root declared in machine.Discovery and returns
// a map of absolute repo path → RepoConfigV2 for every directory that contains
// a .dotkeeper.toml file within the configured depth.
func discoverRepos(machine *config.MachineConfigV2, loadRepo repoLoader) (map[string]*config.RepoConfigV2, error) {
	if loadRepo == nil {
		loadRepo = defaultRepoLoader
	}
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

		if err := walkScanRoot(absRoot, excludeSet, depth, loadRepo, repos); err != nil {
			return nil, fmt.Errorf("walking scan root %s: %w", absRoot, err)
		}
	}

	return repos, nil
}

// walkScanRoot recursively walks dir up to maxDepth levels deep and adds any
// directory containing a .dotkeeper.toml to repos.
func walkScanRoot(
	dir string,
	excludeSet map[string]struct{},
	maxDepth int,
	loadRepo repoLoader,
	repos map[string]*config.RepoConfigV2,
) error {
	if loadRepo == nil {
		loadRepo = defaultRepoLoader
	}
	return walkDir(dir, excludeSet, 0, maxDepth, loadRepo, repos)
}

// walkDir is the recursive core of walkScanRoot.
func walkDir(
	dir string,
	excludeSet map[string]struct{},
	currentDepth, maxDepth int,
	loadRepo repoLoader,
	repos map[string]*config.RepoConfigV2,
) error {
	if _, excluded := excludeSet[dir]; excluded {
		return nil
	}

	// Check if .dotkeeper.toml exists at this level.
	markerPath := config.RepoConfigPath(dir)
	if _, err := os.Stat(markerPath); err == nil {
		repoCfg, err := loadRepo(dir)
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
		if err := walkDir(child, excludeSet, currentDepth+1, maxDepth, loadRepo, repos); err != nil {
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
	GetStatus() (*stclient.SystemStatus, error)
	GetPendingFolders() ([]stclient.PendingFolder, error)
}

// NewObservedProvider returns an ObservedProvider that queries the live
// Syncthing instance (via stClient) and reads state.toml from stateConfigPath.
//
// If stClient is nil or Syncthing is unreachable the returned Observed has
// empty ManagedFolders/LivePeers slices; the error is wrapped with context.
func NewObservedProvider(stClient *stclient.Client, stateConfigPath string) ObservedProvider {
	// Guard against a typed nil: converting a nil *stclient.Client to
	// SyncthingQuerier produces a non-nil interface value, which would
	// bypass the nil check inside newObservedProvider and panic. Explicitly
	// pass a nil interface when the pointer is nil.
	if stClient == nil {
		return newObservedProvider(nil, stateConfigPath, nil, nil, nil, nil)
	}
	return newObservedProvider(stClient, stateConfigPath, nil, nil, nil, nil)
}

// ActivityQuerier is the minimum surface NewObservedProviderWithActivity
// needs from the activity tracker. Returns the most recent observed
// Write/Create/Remove timestamp under root and an ok flag indicating
// whether the path is tracked. Allows the activity package to stay
// out of the reconcile import graph.
type ActivityQuerier interface {
	LastActivity(root string) (time.Time, bool)
}

// HealthQuerier is the minimum surface the provider needs from the
// watchhealth tracker. The signature deliberately returns the
// fields as a small structurally-equivalent FolderHealth so the
// watchhealth package can stay out of reconcile's import graph
// (avoiding a long-term coupling that would force every reconcile
// test to provide a watchhealth stub).
type HealthQuerier interface {
	StatusForReconcile(root string) (FolderHealth, bool)
}

// WakeFlag is consulted by the provider to decide whether
// Observed.SleepWakeSeen should be set on this cycle. Implementations
// are expected to clear the flag on Take() so it fires exactly once
// per detected wake event (Take is "consume the pending flag").
type WakeFlag interface {
	Take() bool
}

// LastRescanQuerier returns the timestamp at which dotkeeper last
// asked Syncthing to rescan the named folder. Returns zero time
// and ok=false when no rescan has been recorded for the path.
type LastRescanQuerier interface {
	LastRescan(path string) (time.Time, bool)
}

// NewObservedProviderWithActivity is NewObservedProvider plus an
// activity querier. The provider populates Observed.LastActivityByPath
// from the querier on every call so Diff can decide auto-pause
// transitions. Pass nil when auto-pause is disabled or unavailable.
func NewObservedProviderWithActivity(stClient *stclient.Client, stateConfigPath string, activity ActivityQuerier) ObservedProvider {
	if stClient == nil {
		return newObservedProvider(nil, stateConfigPath, activity, nil, nil, nil)
	}
	return newObservedProvider(stClient, stateConfigPath, activity, nil, nil, nil)
}

// ObservedProviderInputs bundles the optional v0.9.7 inputs to
// newObservedProvider so the daemon can extend the wiring with
// watchhealth/wake/lastRescan support without further constructor
// proliferation. nil fields are tolerated and Diff degrades
// gracefully (missing watchhealth → all folders treated as
// unreliable; missing wake flag → SleepWakeSeen stays false;
// missing last-rescan → backstops fire on first reconcile, which
// is benign because rescans are idempotent).
type ObservedProviderInputs struct {
	Activity    ActivityQuerier
	Health      HealthQuerier
	Wake        WakeFlag
	LastRescans LastRescanQuerier
}

// NewObservedProviderFull is the v0.9.7 constructor that wires all
// optional inputs. v0.9.6's NewObservedProviderWithActivity
// continues to work as before for callers that only need activity.
func NewObservedProviderFull(stClient *stclient.Client, stateConfigPath string, in ObservedProviderInputs) ObservedProvider {
	if stClient == nil {
		return newObservedProvider(nil, stateConfigPath, in.Activity, in.Health, in.Wake, in.LastRescans)
	}
	return newObservedProvider(stClient, stateConfigPath, in.Activity, in.Health, in.Wake, in.LastRescans)
}

// newObservedProvider accepts the SyncthingQuerier interface directly, which
// lets tests pass a stub without wrapping the real stclient.Client.
func newObservedProvider(
	querier SyncthingQuerier,
	stateConfigPath string,
	activity ActivityQuerier,
	health HealthQuerier,
	wake WakeFlag,
	lastRescans LastRescanQuerier,
) ObservedProvider {
	return func(_ context.Context) (Observed, error) {
		obs := Observed{Now: time.Now()}

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
			// Phase 2: pending folder offers (folders peers have
			// advertised that we haven't accepted). The subscription
			// matcher in Diff reads these. Best-effort — if the
			// pending-folders endpoint errors (older Syncthing
			// versions, transient API issue), we leave the slice
			// nil and the matcher contributes zero acceptances.
			if pf, perr := querier.GetPendingFolders(); perr == nil {
				offered := make([]OfferedFolder, 0, len(pf))
				for _, f := range pf {
					offered = append(offered, OfferedFolder{
						FolderID:     f.ID,
						Label:        f.Label,
						FromDeviceID: f.ReceivedFrom,
					})
				}
				obs.OfferedFolders = offered
			}
		}

		// 1b. Activity timestamps for auto-pause decisions. When the
		// tracker is nil, LastActivityByPath stays nil and Diff
		// skips the Pause/Unpause checks entirely.
		if activity != nil {
			byPath := make(map[string]time.Time, len(obs.ManagedFolders))
			for _, f := range obs.ManagedFolders {
				if t, ok := activity.LastActivity(f.Path); ok {
					byPath[f.Path] = t
				}
			}
			obs.LastActivityByPath = byPath
		}

		// 1c. Watch-health classifications for smart rescan. When
		// the health tracker is nil, WatchHealthByPath stays nil
		// and Diff defaults to "treat every folder as untrusted"
		// (daily backstop), matching the v0.9.4-v0.9.6 baseline.
		if health != nil {
			byPath := make(map[string]FolderHealth, len(obs.ManagedFolders))
			for _, f := range obs.ManagedFolders {
				if h, ok := health.StatusForReconcile(f.Path); ok {
					byPath[f.Path] = h
				}
			}
			obs.WatchHealthByPath = byPath
		}

		// 1d. Wake flag. Take() consumes the pending flag so it
		// fires exactly once per detected suspend/resume cycle.
		if wake != nil {
			obs.SleepWakeSeen = wake.Take()
		}

		// 1e. Last-rescan timestamps for the backstop interval check.
		if lastRescans != nil {
			byPath := make(map[string]time.Time, len(obs.ManagedFolders))
			for _, f := range obs.ManagedFolders {
				if t, ok := lastRescans.LastRescan(f.Path); ok {
					byPath[f.Path] = t
				}
			}
			obs.LastRescanByPath = byPath
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

	status, err := q.GetStatus()
	if err != nil {
		return nil, nil, fmt.Errorf("GetStatus: %w", err)
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
			markerName, _ := fm["markerName"].(string)
			if markerName == "" {
				markerName = stclient.FolderMarkerName
			}

			var devices []string
			if rawDevices, ok := fm["devices"].([]any); ok {
				for _, rd := range rawDevices {
					dm, ok := rd.(map[string]any)
					if !ok {
						continue
					}
					if did, ok := dm["deviceID"].(string); ok && did != "" && did != status.MyID {
						devices = append(devices, did)
					}
				}
			}

			// JSON numerics decode as float64; coerce to int for the
			// drift comparison. Default to 0 (which fails the
			// equality check against the canonical value, correctly
			// flagging the field as drifted) if the key is absent or
			// has the wrong type.
			rescanIntervalS := 0
			if rv, ok := fm["rescanIntervalS"].(float64); ok {
				rescanIntervalS = int(rv)
			}
			fsWatcherEnabled := false
			if fw, ok := fm["fsWatcherEnabled"].(bool); ok {
				fsWatcherEnabled = fw
			}
			// hashers absent in older configs reads as 0, which is
			// also Syncthing's "auto" sentinel. The drift detector
			// distinguishes "explicitly auto" (= 0, would drift to
			// CanonicalHashers=1) from "already canonical" so the
			// migration to hashers=1 runs once per pre-tuning folder.
			hashers := 0
			if h, ok := fm["hashers"].(float64); ok {
				hashers = int(h)
			}
			paused := false
			if p, ok := fm["paused"].(bool); ok {
				paused = p
			}

			folders = append(folders, FolderObs{
				SyncthingFolderID: folderID,
				Path:              folderPath,
				Devices:           devices,
				MarkerDirMissing:  folderMarkerMissing(folderPath, markerName),
				RescanIntervalS:   rescanIntervalS,
				FsWatcherEnabled:  fsWatcherEnabled,
				Hashers:           hashers,
				Paused:            paused,
			})
		}
	}

	// Build LivePeer slice from configured devices, then overlay connection
	// state when Syncthing reports it. Folder sharing requires devices to be
	// present in config, so "configured" is the observed state Diff cares about.
	peersByID := make(map[string]LivePeer)
	if rawDevices, ok := cfg["devices"].([]any); ok {
		for _, rd := range rawDevices {
			dm, ok := rd.(map[string]any)
			if !ok {
				continue
			}
			deviceID, _ := dm["deviceID"].(string)
			if deviceID == "" {
				continue
			}
			peersByID[deviceID] = LivePeer{DeviceID: deviceID}
		}
	}
	if conns != nil {
		for deviceID, conn := range conns.Connections {
			p := peersByID[deviceID]
			p.DeviceID = deviceID
			p.LastSeen = time.Now() // Syncthing REST does not surface last-seen in connections
			p.Connected = conn.Connected
			peersByID[deviceID] = p
		}
	}
	var peers []LivePeer
	for _, p := range peersByID {
		peers = append(peers, p)
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].DeviceID < peers[j].DeviceID
	})

	return folders, peers, nil
}

func folderMarkerMissing(folderPath, markerName string) bool {
	if folderPath == "" || markerName == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(folderPath, markerName))
	if err != nil {
		return true
	}
	return !info.IsDir()
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
// directly. Errors from git (e.g. not a git repo) result in zero values for
// HeadCommit and IsDirty rather than a hard failure — the reconciler should
// still see the repo and can emit appropriate actions.
//
// Performance: a single `git status --porcelain=v2 --branch` call yields
// HEAD oid, dirty state, and the list of changed paths in one subprocess
// instead of three. On a setup with N repos this is 2N fewer git fork+exec
// per reconcile tick.
func queryRepoGitState(repoPath string, state *config.StateV2) RepoObs {
	obs := RepoObs{Path: repoPath}

	obs.HeadCommit, obs.IsDirty, obs.LastChangeAt = readGitState(repoPath)
	obs.IgnoreFileContent = readIgnoreFile(repoPath)
	obs.DevWorkflowActive = isDevWorkflowActive(repoPath)

	if state != nil {
		if sr, ok := state.ObservedRepos[repoPath]; ok {
			obs.LastBackupAt = sr.LastBackupAt
		}
	}

	return obs
}

func readIgnoreFile(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, ".stignore"))
	if err != nil {
		return ""
	}
	return string(data)
}

// isDevWorkflowActive reports whether the user is in the middle of a git
// operation that auto-backup must not interrupt. We look for the marker
// files git itself uses to record in-progress state; checking them is a
// few cheap stat() calls per repo per tick.
//
// The motivation is concrete: dotkeeper's timer-driven `git add -A` +
// `git commit` would otherwise land mid-rebase (creating an "auto:
// scheduled backup" commit between conflict resolutions), mid-merge
// (collapsing the user's MERGE_MSG into a less-informative one), or
// mid-cherry-pick. Deferring the backup costs us at most one tick — the
// next reconcile that observes a quiet repo fires the slot normally,
// still within the configured interval.
func isDevWorkflowActive(repoPath string) bool {
	// Resolve the git dir once. Most repos have .git as a directory at
	// the worktree root; submodules and worktrees use .git files that
	// point elsewhere. We honour that with a single git invocation.
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir")
	out, err := procnice.Output(cmd)
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}
	for _, marker := range []string{
		"rebase-merge",     // interactive rebase or `git merge --rebase`
		"rebase-apply",     // `git am` / classic rebase
		"MERGE_HEAD",       // `git merge` paused on conflict
		"CHERRY_PICK_HEAD", // `git cherry-pick` paused
		"REVERT_HEAD",      // `git revert` paused
		"BISECT_LOG",       // `git bisect` in progress
	} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return true
		}
	}
	return false
}

// readGitState shells out once to `git status --porcelain=v2 --branch` and
// returns (HEAD oid, dirty flag, most-recent mtime among dirty paths). Zero
// values are returned on any error so the caller treats this as "repo
// present but no info" — consistent with the pre-collapse helpers.
func readGitState(repoPath string) (head string, dirty bool, lastChange time.Time) {
	cmd := exec.Command("git", "status", "--porcelain=v2", "--branch")
	cmd.Dir = repoPath
	out, err := procnice.Output(cmd)
	if err != nil {
		return "", false, time.Time{}
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	// Status output for big dirty repos can exceed the default 64K line cap
	// when a single entry's path is long. Bump the buffer.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if line[0] == '#' {
			if head == "" && bytes.HasPrefix(line, []byte("# branch.oid ")) {
				head = string(bytes.TrimSpace(line[len("# branch.oid "):]))
				if head == "(initial)" {
					head = ""
				}
			}
			continue
		}
		dirty = true
		path := porcelainV2Path(line)
		if path == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(repoPath, path))
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().After(lastChange) {
			lastChange = info.ModTime()
		}
	}
	return head, dirty, lastChange
}

// porcelainV2Path extracts the working-tree path from one status entry line.
// Format reference: git-status(1) "Porcelain Format Version 2". We only need
// the path for stat-based mtime collection, not the rest of the entry's
// metadata, so this parser is intentionally minimal.
func porcelainV2Path(line []byte) string {
	if len(line) < 2 {
		return ""
	}
	switch line[0] {
	case '?', '!':
		// "? <path>" — single field after the prefix.
		return string(line[2:])
	case '1', 'u':
		// "1 XY sub mH mI mW hH hI hM <path>" — path is the 9th field
		// for "1" entries; "u" entries have the same trailing path slot.
		return fieldFromN(line, 8)
	case '2':
		// "2 XY sub mH mI mW hH hI X<score> <path>\t<origPath>" — we want
		// the new path (before the tab).
		rest := fieldFromN(line, 9)
		if i := strings.IndexByte(rest, '\t'); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// fieldFromN returns line from the start of its n-th space-separated field
// (0-indexed) to end of line. So fieldFromN("a b c d", 2) == "c d". Returns
// "" if line has fewer than n+1 fields. Used to grab the path tail of a
// porcelain-v2 entry without losing internal spaces in the path.
func fieldFromN(line []byte, n int) string {
	idx := 0
	for i := 0; i < n; i++ {
		sp := bytes.IndexByte(line[idx:], ' ')
		if sp < 0 {
			return ""
		}
		idx += sp + 1
	}
	if idx >= len(line) {
		return ""
	}
	return string(line[idx:])
}

// gitHeadCommit returns the current HEAD commit hash, or empty string on
// error. Used by applier.markRepoPushed after a successful push, where we
// don't need the full status output — just the new HEAD.
func gitHeadCommit(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := procnice.Output(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

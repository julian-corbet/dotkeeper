// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
// It provides pure-function diff over (Desired, Observed) state, a set of
// Action types, and a Reconciler that drives the diff→apply loop.
//
// Schema types (MachineConfigV2, RepoConfigV2, StateV2, etc.) are imported
// from internal/config.
package reconcile

import (
	"slices"
	"strings"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// Desired represents the declarative configuration for this machine: what
// repos should be tracked, what Syncthing folders should exist, and which
// peers should be known.
type Desired struct {
	// MachineName is the stable identifier for this machine (from machine.toml).
	MachineName string

	// Repos maps repo path to its desired state.
	Repos map[string]RepoDesired

	// Peers is the list of devices that should be peered with this machine.
	Peers []PeerDesired
}

// RepoDesired is the desired state for a single tracked repository.
type RepoDesired struct {
	// Path is the absolute filesystem path of the git repository.
	Path string

	// SyncthingFolderID is the Syncthing folder ID that backs this repo.
	SyncthingFolderID string

	// Ignore is the list of patterns to exclude from Syncthing sync.
	Ignore []string

	// ShareWith is the list of peer device IDs this folder should be shared with.
	ShareWith []string

	// CommitPolicy controls whether and when dotkeeper may auto-commit and push.
	CommitPolicy string

	// IdleSeconds is the quiet period required by the "on-idle" policy.
	IdleSeconds uint

	// GitInterval controls the "timer" policy and push cadence.
	GitInterval string

	// SkipSlots lists machine slots that should not run git backups.
	SkipSlots []uint

	// MachineSlot is copied from machine.toml for skip-slot evaluation.
	MachineSlot uint
}

// PeerDesired is the desired state for a single Syncthing peer.
type PeerDesired struct {
	// Name is the human-readable machine name for this peer.
	Name string

	// DeviceID is the Syncthing device ID for this peer.
	DeviceID string
}

// Observed represents the current live state of the system as queried from
// Syncthing and git.
type Observed struct {
	// ManagedFolders is the list of Syncthing folders currently configured.
	ManagedFolders []FolderObs

	// TrackedRepos is the list of git repos currently tracked by dotkeeper.
	TrackedRepos []RepoObs

	// LivePeers is the list of Syncthing devices currently known.
	LivePeers []LivePeer

	// CachedState is the last-written state.toml snapshot. It provides
	// per-repo push history so Diff can skip pushes for commits already backed
	// up. May be nil if the state file has not been written yet.
	CachedState *config.StateV2
}

// BuildDesired constructs a Desired from a parsed MachineConfigV2, the
// per-repo configs keyed by absolute repo path, and the current StateV2.
// It is the canonical way to translate the on-disk configuration into the
// form expected by Diff.
//
// repos may be nil or empty (e.g. on first run before discovery has run).
// state may be nil; when nil, no peers are populated.
func BuildDesired(machine *config.MachineConfigV2, repos map[string]*config.RepoConfigV2, state *config.StateV2) Desired {
	d := Desired{
		Repos: make(map[string]RepoDesired, len(repos)),
	}
	if machine != nil {
		d.MachineName = machine.Name
	}
	// Peers may be declared in machine.toml (Home Manager/Nix friendly) and/or
	// added imperatively to state.toml. Merge both sources by device ID.
	peersByName := make(map[string]string)
	seenPeerIDs := make(map[string]bool)
	addPeer := func(p config.PeerEntry) {
		if p.Name == "" || p.DeviceID == "" || seenPeerIDs[p.DeviceID] {
			return
		}
		seenPeerIDs[p.DeviceID] = true
		peersByName[p.Name] = p.DeviceID
		d.Peers = append(d.Peers, PeerDesired{Name: p.Name, DeviceID: p.DeviceID})
	}
	if machine != nil {
		for _, p := range machine.Peers {
			addPeer(p)
		}
	}
	if state != nil {
		for _, p := range state.Peers {
			addPeer(p)
		}
	}

	defaultCommitPolicy := "manual"
	defaultGitInterval := "hourly"
	var machineSlot uint
	if machine != nil {
		defaultCommitPolicy = machine.DefaultCommitPolicy
		defaultGitInterval = machine.DefaultGitInterval
		machineSlot = machine.Slot
	}
	for path, r := range repos {
		if r == nil {
			continue
		}
		// Defensive copy: avoid slice aliasing with the source config. Sort or
		// append on a RepoDesired must never mutate another RepoDesired or the
		// original MachineConfigV2 / RepoConfigV2.
		shareWith := append([]string(nil), r.Sync.ShareWith...)
		if len(shareWith) == 0 && machine != nil {
			shareWith = append([]string(nil), machine.DefaultShareWith...)
		}
		shareWith = resolveShareWith(shareWith, d.Peers, peersByName)
		ignore := config.MergeSyncIgnorePatterns(r.Sync.Ignore)
		commitPolicy := r.Commit.Policy
		if commitPolicy == "" {
			commitPolicy = defaultCommitPolicy
		}
		gitInterval := r.GitBackup.Interval
		if gitInterval == "" {
			gitInterval = defaultGitInterval
		}
		d.Repos[path] = RepoDesired{
			Path:              path,
			SyncthingFolderID: r.Sync.SyncthingFolderID,
			Ignore:            ignore,
			ShareWith:         shareWith,
			CommitPolicy:      commitPolicy,
			IdleSeconds:       r.Commit.IdleSeconds,
			GitInterval:       gitInterval,
			SkipSlots:         append([]uint(nil), r.GitBackup.SkipSlots...),
			MachineSlot:       machineSlot,
		}
	}
	return d
}

func resolveShareWith(namesOrIDs []string, peers []PeerDesired, peersByName map[string]string) []string {
	if len(namesOrIDs) == 0 {
		out := make([]string, 0, len(peers))
		for _, p := range peers {
			out = append(out, p.DeviceID)
		}
		return out
	}

	out := make([]string, 0, len(namesOrIDs))
	seen := make(map[string]bool, len(namesOrIDs))
	for _, item := range namesOrIDs {
		deviceID := ""
		if resolved, ok := peersByName[item]; ok {
			deviceID = resolved
		} else if looksLikeDeviceID(item) {
			deviceID = item
		}
		if deviceID == "" || seen[deviceID] {
			continue
		}
		seen[deviceID] = true
		out = append(out, deviceID)
	}
	return out
}

func looksLikeDeviceID(s string) bool {
	if len(s) < 7 || !strings.Contains(s, "-") {
		return false
	}
	for _, r := range s {
		if r == '-' || (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			continue
		}
		return false
	}
	return true
}

// FolderObs is the observed state of a single Syncthing folder.
type FolderObs struct {
	// SyncthingFolderID is the Syncthing folder ID.
	SyncthingFolderID string

	// Path is the filesystem path of the folder root.
	Path string

	// Devices is the list of device IDs this folder is shared with.
	Devices []string

	// MarkerDirMissing reports that Syncthing's configured folder marker
	// directory is absent from Path. A missing marker leaves the folder
	// configured but unusable, so reconcile must repair it.
	MarkerDirMissing bool
}

// RepoObs is the observed state of a single tracked git repository.
type RepoObs struct {
	// Path is the absolute filesystem path of the repository.
	Path string

	// HeadCommit is the current HEAD commit hash (empty if unknown).
	HeadCommit string

	// IsDirty reports whether the working tree has uncommitted changes.
	IsDirty bool

	// LastBackupAt is when this repo was last successfully pushed to a remote.
	LastBackupAt time.Time

	// LastChangeAt is the newest modification time among dirty working-tree
	// files. It is zero when the repo is clean or the timestamp is unknown.
	LastChangeAt time.Time

	// IgnoreFileContent is the current .stignore content for the repo root.
	// Empty means the file is absent or unreadable.
	IgnoreFileContent string
}

// LivePeer is the observed connection state of a single Syncthing device.
type LivePeer struct {
	// DeviceID is the Syncthing device ID.
	DeviceID string

	// LastSeen is when this device was last observed by Syncthing.
	LastSeen time.Time

	// Connected reports whether this device is currently online.
	Connected bool
}

// DesiredForPath returns the desired repo settings for path, or false when
// the path is not managed by dotkeeper's declarative config.
func (d Desired) DesiredForPath(path string) (RepoDesired, bool) {
	if d.Repos == nil {
		return RepoDesired{}, false
	}
	r, ok := d.Repos[path]
	return r, ok
}

func (r RepoDesired) slotSkipped() bool {
	return slices.Contains(r.SkipSlots, r.MachineSlot)
}

// Action describes a single idempotent side-effect that the reconciler should
// perform to move observed state towards desired state.
type Action interface {
	// Describe returns a human-readable one-line description of the action.
	Describe() string
}

// AddSyncthingFolder is emitted when a Syncthing folder needs to be created.
type AddSyncthingFolder struct {
	FolderID string
	Path     string
	Devices  []string
}

func (a AddSyncthingFolder) Describe() string {
	return "add Syncthing folder " + a.FolderID + " at " + a.Path
}

// RemoveSyncthingFolder is emitted when a Syncthing folder should be removed.
type RemoveSyncthingFolder struct {
	FolderID string
}

func (a RemoveSyncthingFolder) Describe() string {
	return "remove Syncthing folder " + a.FolderID
}

// UpdateSyncthingFolderDevices is emitted when the device list for an existing
// Syncthing folder differs from desired.
type UpdateSyncthingFolderDevices struct {
	FolderID string
	Devices  []string
}

func (a UpdateSyncthingFolderDevices) Describe() string {
	return "update devices for Syncthing folder " + a.FolderID
}

// EnsureIgnoreFile is emitted when a repo root is missing dotkeeper's
// canonical .stignore file or the content has drifted.
type EnsureIgnoreFile struct {
	RepoPath string
	Patterns []string
}

func (a EnsureIgnoreFile) Describe() string {
	return "ensure Syncthing ignores for " + a.RepoPath
}

// EnsureFolderMarker is emitted when a configured Syncthing folder is missing
// dotkeeper's local marker directory.
type EnsureFolderMarker struct {
	RepoPath string
}

func (a EnsureFolderMarker) Describe() string {
	return "ensure Syncthing folder marker for " + a.RepoPath
}

// AddSyncthingDevice is emitted when a desired peer is missing from the
// Syncthing device roster.
type AddSyncthingDevice struct {
	Name     string
	DeviceID string
}

func (a AddSyncthingDevice) Describe() string {
	return "add Syncthing peer " + a.Name
}

// GitCommitDirty is emitted when a tracked repo has uncommitted changes that
// should be auto-committed.
type GitCommitDirty struct {
	RepoPath string
	Message  string
}

func (a GitCommitDirty) Describe() string {
	return "git commit dirty repo " + a.RepoPath
}

// GitPushRepo is emitted when a tracked repo has commits that should be pushed.
type GitPushRepo struct {
	RepoPath string
}

func (a GitPushRepo) Describe() string {
	return "git push repo " + a.RepoPath
}

// TrackRepo is emitted when a path should be registered as a tracked repo.
type TrackRepo struct {
	Path string
}

func (a TrackRepo) Describe() string {
	return "track repo " + a.Path
}

// UntrackRepo is emitted when a path should be deregistered as a tracked repo.
type UntrackRepo struct {
	Path string
}

func (a UntrackRepo) Describe() string {
	return "untrack repo " + a.Path
}

// Plan is an ordered list of Actions produced by Diff and consumed by Applier.
type Plan []Action

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
// It provides pure-function diff over (Desired, Observed) state, a set of
// Action types, and a Reconciler that drives the diff→apply loop.
//
// Schema types (MachineConfigV2, StateV2, etc.) live on the parallel branch
// v0.5/schema-types and are represented here by local stubs.
package reconcile

import "time"

// TODO(v0.5/schema-types): Replace all stubs in this file with types from
// the schema-types package once that branch is merged.

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
}

// FolderObs is the observed state of a single Syncthing folder.
type FolderObs struct {
	// SyncthingFolderID is the Syncthing folder ID.
	SyncthingFolderID string

	// Path is the filesystem path of the folder root.
	Path string

	// Devices is the list of device IDs this folder is shared with.
	Devices []string
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

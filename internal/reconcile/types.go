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

	// Subscriptions is the merged subscription list (declarative
	// machine.toml + imperative state.toml). Phase 2: reconcile
	// matches these against offered folders from peers' ClusterConfigs
	// and emits AcceptSubscription actions for matches. nil/empty
	// → subscription matching is a no-op.
	Subscriptions []config.SubscriptionEntry

	// DefaultMirrorPath, when non-empty, is the scan root where
	// newly-accepted subscriptions land by convention:
	// <DefaultMirrorPath>/<folder-name-from-canonical>. Empty
	// falls back to "~/Documents/GitHub" so we never write to an
	// undefined location.
	DefaultMirrorPath string
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

	// GitCanonical is the canonical git-remote identity for this
	// repo, derived from .git/config's origin URL at track time.
	// Empty for non-git folders. Reconcile uses it as the
	// Syncthing folder label so peers' ClusterConfigs carry the
	// load-bearing identity needed for subscription matching.
	GitCanonical string
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

	// LastActivityByPath maps each managed folder root path to the
	// most recent Write/Create/Remove timestamp observed by the
	// dotkeeper-side activity tracker. nil when the tracker is
	// unavailable (auto-pause disabled). Diff uses the timestamps to
	// decide PauseSyncthingFolder / UnpauseSyncthingFolder actions.
	LastActivityByPath map[string]time.Time

	// WatchHealthByPath maps each managed folder root path to its
	// current watchhealth.Status. nil when the watchhealth tracker
	// is unavailable; in that case Diff falls back to per-folder
	// periodic rescan (as if every folder were on an unreliable
	// filesystem), matching pre-v0.9.7 behaviour.
	//
	// The map is encoded as a generic key-value pair rather than a
	// typed reference to watchhealth.Status because importing
	// watchhealth into reconcile would create a long-term coupling:
	// reconcile-side tests would need a watchhealth stub for every
	// scenario. Instead, the diff treats WatchHealthByPath as opaque
	// data (uses only the fields it knows by name via Status struct
	// embedding through the FolderHealth re-export below).
	WatchHealthByPath map[string]FolderHealth

	// SleepWakeSeen reports whether the wake detector signalled a
	// suspect resume-from-sleep event since the last reconcile
	// cycle. When true, Diff emits a RescanFolderNow for every
	// managed folder regardless of its filesystem classification.
	SleepWakeSeen bool

	// LastRescanByPath tracks when dotkeeper last asked Syncthing
	// to rescan each folder. Drives the weekly-backstop check for
	// reliable filesystems and the daily-backstop check for
	// unreliable filesystems. May be nil on first reconcile after
	// daemon startup; Diff treats nil as "never rescanned" and
	// emits rescans where due.
	LastRescanByPath map[string]time.Time

	// Now is the wall-clock time at which Observed was constructed.
	// Diff uses it for "is this older than the idle threshold"
	// comparisons. Carrying it on the struct rather than calling
	// time.Now() inside Diff keeps Diff a pure function — same inputs
	// always produce the same plan.
	Now time.Time

	// OfferedFolders is the list of folders peers have advertised
	// via ClusterConfig but the local Syncthing hasn't accepted.
	// Populated from Syncthing's /rest/cluster/pending/folders API.
	// Phase 2 subscription matcher reads this; legacy callers can
	// leave nil and the matcher will produce zero acceptances.
	OfferedFolders []OfferedFolder

	// PeerNameByID maps Syncthing device ID → human-readable
	// peer name so the subscription matcher can render
	// operator-friendly status strings. Populated from
	// machine.toml + state.toml's peer rosters.
	PeerNameByID map[string]string
}

// OfferedFolder is a folder that a peer has advertised via
// ClusterConfig but the local Syncthing hasn't accepted yet.
// Phase 2 subscription matching reads these.
type OfferedFolder struct {
	// FolderID is the Syncthing folder ID the offering peer assigned.
	FolderID string
	// Label is what the offering peer set on the folder. dotkeeper
	// v1.2+ peers set this to the canonical git-remote URL.
	Label string
	// FromDeviceID is the offering peer's Syncthing device ID.
	FromDeviceID string
}

// FolderHealth is reconcile's view of the watchhealth.Status for
// one folder. Kept as a structurally-equivalent copy so the diff
// can branch on health without taking on a hard dependency on the
// watchhealth package's representation. The provider populates it
// by copying fields one-to-one from watchhealth.Status.
type FolderHealth struct {
	FilesystemReliable  bool
	OverflowSeen        bool
	WatchLimitHit       bool
	LastReliableEventAt time.Time
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

	// Subscriptions merge: machine.toml (declarative) + state.toml
	// (imperative). Declarative wins on conflict — same precedence
	// as Peers above.
	var declSubs, impSubs []config.SubscriptionEntry
	if machine != nil {
		declSubs = machine.Subscribe
	}
	if state != nil {
		impSubs = state.Subscriptions
	}
	d.Subscriptions = config.MergeSubscriptions(declSubs, impSubs)
	// DefaultMirrorPath: first configured scan root, expanded.
	// Used by Diff to place subscriber-side folders that don't
	// have an explicit path. Falls back to "~/Documents/GitHub"
	// in the helper itself.
	if machine != nil && len(machine.Discovery.ScanRoots) > 0 {
		d.DefaultMirrorPath = machine.Discovery.ScanRoots[0]
	}

	defaultCommitPolicy := "manual"
	defaultGitInterval := "daily"
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
			GitCanonical:      r.Git.Canonical,
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

	// RescanIntervalS is the folder's current rescanIntervalS as
	// reported by Syncthing's REST config. Compared against
	// stclient.CanonicalRescanIntervalS so reconcile can migrate
	// folders carried over from earlier dotkeeper installs whose
	// scheduler values predate the v0.9.4 default change.
	RescanIntervalS int

	// FsWatcherEnabled mirrors the folder's fsWatcherEnabled field.
	// Part of the same scheduler-drift check as RescanIntervalS.
	FsWatcherEnabled bool

	// Hashers mirrors the folder's `hashers` field (the number of
	// parallel hash workers Syncthing spawns during a scan). 0
	// means "auto = min(GOMAXPROCS, 8)" — the upstream default and
	// the pre-dotkeeper-tuning behaviour. dotkeeper sets this to 1
	// canonically to cap the peak CPU spike during cold-start /
	// wake-from-suspend rescans, where 30 folders cold-scanning
	// simultaneously would otherwise pin 8 cores briefly.
	Hashers int

	// Paused mirrors the folder's `paused` field. When true, Syncthing
	// runs no scanner, no fsWatcher, and no BEP gossip for the folder.
	// The auto-pause feature in v0.9.6 toggles this based on observed
	// filesystem activity from the dotkeeper-side activity tracker.
	Paused bool
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

	// DevWorkflowActive reports whether the user is mid-rebase, mid-merge,
	// mid-cherry-pick, or mid-bisect at the time of observation. When true,
	// auto-backup defers this repo to the next reconcile tick so dotkeeper
	// does not interleave its own commit with the user's in-progress work.
	// Slot scheduling is unaffected: the next tick that observes a quiet
	// repo will fire the backup, still within the configured interval.
	DevWorkflowActive bool
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
	// Label is the human-readable identifier Syncthing displays
	// for this folder. When the repo is git-backed dotkeeper sets
	// this to the canonical-URL identity (e.g.
	// "github.com/julian-corbet/dotkeeper") so the Syncthing UI
	// and the dotkeeper discovery surface both show something
	// meaningful. Empty falls back to FolderID (legacy behaviour
	// — non-git folders or pre-v1.2 installs).
	Label string
}

func (a AddSyncthingFolder) Describe() string {
	return "add Syncthing folder " + a.FolderID + " at " + a.Path
}

// AcceptSubscription is emitted when a declared subscription
// matches an offered folder from a peer. The applier:
//  1. Materialises Path (mkdir + optional git clone if Canonical
//     names a reachable remote and Path doesn't already exist)
//  2. Writes the local .dotkeeper.toml so the next discovery scan
//     re-finds the folder via the normal path
//  3. Adds the folder to Syncthing's config with the offering
//     peer in share-with
//
// This is one-shot per subscription per folder ID: after the first
// successful accept, subsequent reconciles see the folder in
// observed.ManagedFolders and skip re-emitting.
type AcceptSubscription struct {
	// FolderID is the Syncthing folder ID the peer assigned.
	FolderID string
	// Label is the canonical identity (URL or "dk:<name>") to
	// stamp on the local folder so other peers see the same
	// identity once we re-share it.
	Label string
	// Path is the absolute local path where the folder will
	// land. Caller (Diff) computed this from the subscription's
	// Path field or the mirror-convention default.
	Path string
	// FromDeviceID is the offering peer; reconcile adds them to
	// the new folder's share-with so initial sync starts from
	// them.
	FromDeviceID string
	// FromPeerName is for log/diagnostic strings only.
	FromPeerName string
	// CloneRemote, when non-empty, is the git URL the applier
	// should clone into Path before adding the Syncthing folder.
	// Empty: skip cloning (path already exists OR caller wants
	// Syncthing to seed the working tree directly).
	CloneRemote string
}

func (a AcceptSubscription) Describe() string {
	if a.CloneRemote != "" {
		return "accept subscription " + a.Label + " (clone+mount " + a.Path + ")"
	}
	return "accept subscription " + a.Label + " (mount " + a.Path + ")"
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

// UpdateSyncthingFolderSchedule is emitted when the folder's scheduler
// fields (rescanIntervalS, fsWatcherEnabled) drift from the canonical
// dotkeeper-managed values. The driving case is migrating folders
// carried over from v0.9.3-and-earlier installs whose
// rescanIntervalS=60 still bleeds CPU after upgrade because
// reconcile's other diff checks see "folder exists, correct path,
// correct devices, marker present" and emit no actions.
type UpdateSyncthingFolderSchedule struct {
	FolderID string
}

func (a UpdateSyncthingFolderSchedule) Describe() string {
	return "update scheduler fields for Syncthing folder " + a.FolderID
}

// PauseSyncthingFolder is emitted by the auto-pause logic when a
// folder has been quiet on the local filesystem for longer than the
// idle threshold. Pausing stops Syncthing's scanner, watcher, and BEP
// gossip for the folder; the index DB is unloaded from memory.
// Unpause happens automatically on the next reconcile after the
// activity tracker sees a Write/Create/Remove under the folder root.
type PauseSyncthingFolder struct {
	FolderID string
}

func (a PauseSyncthingFolder) Describe() string {
	return "pause idle Syncthing folder " + a.FolderID
}

// UnpauseSyncthingFolder is the inverse of PauseSyncthingFolder.
// Emitted when a paused folder sees activity. Reconcile is expected
// to fire shortly after the activity tracker observes the event, so
// the user-perceived pause-to-unpause latency is dominated by the
// debounce window inside the reconcile loop (~1 second) rather than
// the auto-pause interval.
type UnpauseSyncthingFolder struct {
	FolderID string
}

func (a UnpauseSyncthingFolder) Describe() string {
	return "unpause active Syncthing folder " + a.FolderID
}

// RescanFolderNow asks Syncthing to do an immediate full rescan of
// the folder identified by FolderID. Emitted by the v0.9.7 smart
// rescan logic when the OS event API is suspected to have missed
// something. Reason is included for log evidence — operators see
// "rescan folder X (reason: inotify queue overflow)" rather than
// having to deduce why the action fired.
//
// Path is included because the applier needs to call back into the
// watchhealth tracker's Reset(path) after a successful rescan, and
// the tracker is keyed by path, not folder ID.
type RescanFolderNow struct {
	FolderID string
	Path     string
	Reason   string
}

func (a RescanFolderNow) Describe() string {
	if a.Reason == "" {
		return "rescan Syncthing folder " + a.FolderID
	}
	return "rescan Syncthing folder " + a.FolderID + " (reason: " + a.Reason + ")"
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

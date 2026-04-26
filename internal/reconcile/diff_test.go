// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

func TestDiff(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name    string
		desired Desired
		obs     Observed
		check   func(t *testing.T, plan Plan)
	}{
		{
			name:    "empty desired empty observed yields empty plan",
			desired: Desired{},
			obs:     Observed{},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 0 {
					t.Fatalf("expected empty plan, got %d actions", len(plan))
				}
			},
		},
		{
			name: "desired folder not in observed → AddSyncthingFolder",
			desired: Desired{
				Repos: map[string]RepoDesired{
					"/home/user/dotfiles": {
						Path:              "/home/user/dotfiles",
						SyncthingFolderID: "dk-dotfiles",
						ShareWith:         []string{"DEVICE-A"},
					},
				},
			},
			obs: Observed{},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				a, ok := plan[0].(AddSyncthingFolder)
				if !ok {
					t.Fatalf("expected AddSyncthingFolder, got %T", plan[0])
				}
				if a.FolderID != "dk-dotfiles" {
					t.Errorf("wrong folder ID: %q", a.FolderID)
				}
				if a.Path != "/home/user/dotfiles" {
					t.Errorf("wrong path: %q", a.Path)
				}
			},
		},
		{
			name:    "observed folder not in desired → RemoveSyncthingFolder",
			desired: Desired{},
			obs: Observed{
				ManagedFolders: []FolderObs{
					{SyncthingFolderID: "stale-folder", Path: "/old/path"},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				a, ok := plan[0].(RemoveSyncthingFolder)
				if !ok {
					t.Fatalf("expected RemoveSyncthingFolder, got %T", plan[0])
				}
				if a.FolderID != "stale-folder" {
					t.Errorf("wrong folder ID: %q", a.FolderID)
				}
			},
		},
		{
			name: "folder exists but devices differ → UpdateSyncthingFolderDevices",
			desired: Desired{
				Repos: map[string]RepoDesired{
					"/repo": {
						Path:              "/repo",
						SyncthingFolderID: "dk-repo",
						ShareWith:         []string{"DEVICE-A", "DEVICE-B"},
					},
				},
			},
			obs: Observed{
				ManagedFolders: []FolderObs{
					{
						SyncthingFolderID: "dk-repo",
						Path:              "/repo",
						Devices:           []string{"DEVICE-A"}, // missing DEVICE-B
					},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				a, ok := plan[0].(UpdateSyncthingFolderDevices)
				if !ok {
					t.Fatalf("expected UpdateSyncthingFolderDevices, got %T", plan[0])
				}
				if a.FolderID != "dk-repo" {
					t.Errorf("wrong folder ID: %q", a.FolderID)
				}
			},
		},
		{
			name: "folder and devices already match → no action",
			desired: Desired{
				Repos: map[string]RepoDesired{
					"/repo": {
						Path:              "/repo",
						SyncthingFolderID: "dk-repo",
						ShareWith:         []string{"DEVICE-A"},
					},
				},
			},
			obs: Observed{
				ManagedFolders: []FolderObs{
					{
						SyncthingFolderID: "dk-repo",
						Path:              "/repo",
						Devices:           []string{"DEVICE-A"},
					},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 0 {
					t.Fatalf("expected empty plan, got %d actions", len(plan))
				}
			},
		},
		{
			name: "idempotency: applying diff on already-consistent state is a no-op",
			desired: Desired{
				Repos: map[string]RepoDesired{
					"/dots": {Path: "/dots", SyncthingFolderID: "dk-dots", ShareWith: []string{"X", "Y"}},
				},
			},
			obs: Observed{
				ManagedFolders: []FolderObs{
					{SyncthingFolderID: "dk-dots", Path: "/dots", Devices: []string{"X", "Y"}},
				},
			},
			check: func(t *testing.T, plan Plan) {
				for _, a := range plan {
					switch a.(type) {
					case AddSyncthingFolder, RemoveSyncthingFolder, UpdateSyncthingFolderDevices:
						t.Errorf("unexpected folder action on already-consistent state: %s", a.Describe())
					}
				}
			},
		},
		{
			name:    "dirty repo → GitCommitDirty",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/home/user/notes", IsDirty: true, HeadCommit: ""},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				a, ok := plan[0].(GitCommitDirty)
				if !ok {
					t.Fatalf("expected GitCommitDirty, got %T", plan[0])
				}
				if a.RepoPath != "/home/user/notes" {
					t.Errorf("wrong repo path: %q", a.RepoPath)
				}
				if a.Message == "" {
					t.Error("commit message must not be empty")
				}
			},
		},
		{
			name:    "repo with head commit → GitPushRepo",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/home/user/code", IsDirty: false, HeadCommit: "abc123"},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				_, ok := plan[0].(GitPushRepo)
				if !ok {
					t.Fatalf("expected GitPushRepo, got %T", plan[0])
				}
			},
		},
		{
			name:    "dirty repo with head commit → commit then push",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/repo", IsDirty: true, HeadCommit: "def456"},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 2 {
					t.Fatalf("expected 2 actions, got %d", len(plan))
				}
				if _, ok := plan[0].(GitCommitDirty); !ok {
					t.Errorf("first action should be GitCommitDirty, got %T", plan[0])
				}
				if _, ok := plan[1].(GitPushRepo); !ok {
					t.Errorf("second action should be GitPushRepo, got %T", plan[1])
				}
			},
		},
		{
			name:    "clean repo with empty head commit → no git actions",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/repo", IsDirty: false, HeadCommit: ""},
				},
			},
			check: func(t *testing.T, plan Plan) {
				for _, a := range plan {
					switch a.(type) {
					case GitCommitDirty, GitPushRepo:
						t.Errorf("unexpected git action for clean untracked repo: %s", a.Describe())
					}
				}
			},
		},
		{
			name:    "multiple repos produce actions in path-sorted order",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/z/repo", IsDirty: true, HeadCommit: "", LastBackupAt: now},
					{Path: "/a/repo", IsDirty: true, HeadCommit: "", LastBackupAt: now},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) < 2 {
					t.Fatalf("expected at least 2 actions, got %d", len(plan))
				}
				// /a/repo comes before /z/repo
				first := plan[0].(GitCommitDirty)
				second := plan[1].(GitCommitDirty)
				if first.RepoPath != "/a/repo" {
					t.Errorf("expected /a/repo first, got %q", first.RepoPath)
				}
				if second.RepoPath != "/z/repo" {
					t.Errorf("expected /z/repo second, got %q", second.RepoPath)
				}
			},
		},
		{
			name: "repo with no Syncthing folder ID is ignored for folder diff",
			desired: Desired{
				Repos: map[string]RepoDesired{
					"/bare": {Path: "/bare", SyncthingFolderID: ""},
				},
			},
			obs: Observed{},
			check: func(t *testing.T, plan Plan) {
				for _, a := range plan {
					if _, ok := a.(AddSyncthingFolder); ok {
						t.Error("should not add folder for repo with empty SyncthingFolderID")
					}
				}
			},
		},
		{
			name:    "repo already pushed at HEAD → no GitPushRepo",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/repo", IsDirty: false, HeadCommit: "abc123"},
				},
				CachedState: &config.StateV2{
					ObservedRepos: map[string]config.ObservedRepo{
						"/repo": {LastPushedCommit: "abc123"},
					},
				},
			},
			check: func(t *testing.T, plan Plan) {
				for _, a := range plan {
					if _, ok := a.(GitPushRepo); ok {
						t.Error("should not push when HEAD already matches last pushed commit")
					}
				}
			},
		},
		{
			name:    "repo HEAD differs from last pushed → GitPushRepo",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/repo", IsDirty: false, HeadCommit: "newcommit"},
				},
				CachedState: &config.StateV2{
					ObservedRepos: map[string]config.ObservedRepo{
						"/repo": {LastPushedCommit: "oldcommit"},
					},
				},
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				if _, ok := plan[0].(GitPushRepo); !ok {
					t.Fatalf("expected GitPushRepo, got %T", plan[0])
				}
			},
		},
		{
			name:    "repo with head commit and nil CachedState → GitPushRepo",
			desired: Desired{},
			obs: Observed{
				TrackedRepos: []RepoObs{
					{Path: "/repo", IsDirty: false, HeadCommit: "abc123"},
				},
				CachedState: nil,
			},
			check: func(t *testing.T, plan Plan) {
				if len(plan) != 1 {
					t.Fatalf("expected 1 action, got %d", len(plan))
				}
				if _, ok := plan[0].(GitPushRepo); !ok {
					t.Fatalf("expected GitPushRepo, got %T", plan[0])
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := Diff(tc.desired, tc.obs)
			tc.check(t, plan)
		})
	}
}

// TestBuildDesired verifies that BuildDesired correctly translates
// MachineConfigV2 + per-repo RepoConfigV2 maps into a Desired value.
func TestBuildDesired(t *testing.T) {
	t.Parallel()

	t.Run("nil machine and empty repos yields zero Desired", func(t *testing.T) {
		t.Parallel()
		d := BuildDesired(nil, nil, nil)
		if d.MachineName != "" {
			t.Errorf("expected empty MachineName, got %q", d.MachineName)
		}
		if len(d.Repos) != 0 {
			t.Errorf("expected empty Repos, got %d", len(d.Repos))
		}
		if len(d.Peers) != 0 {
			t.Errorf("expected empty Peers, got %d", len(d.Peers))
		}
	})

	t.Run("machine name is set from machine config; peers come from state", func(t *testing.T) {
		t.Parallel()
		machine := &config.MachineConfigV2{
			Name:             "elitebook",
			DefaultShareWith: []string{"desktop", "server"},
		}
		state := &config.StateV2{
			Peers: []config.PeerEntry{
				{Name: "desktop", DeviceID: "DEV-D"},
				{Name: "server", DeviceID: "DEV-S"},
			},
		}
		d := BuildDesired(machine, nil, state)
		if d.MachineName != "elitebook" {
			t.Errorf("expected MachineName=elitebook, got %q", d.MachineName)
		}
		if len(d.Peers) != 2 {
			t.Fatalf("expected 2 peers, got %d", len(d.Peers))
		}
		if d.Peers[0].Name != "desktop" || d.Peers[0].DeviceID != "DEV-D" {
			t.Errorf("unexpected peer[0]: %+v", d.Peers[0])
		}
		if d.Peers[1].Name != "server" || d.Peers[1].DeviceID != "DEV-S" {
			t.Errorf("unexpected peer[1]: %+v", d.Peers[1])
		}
	})

	t.Run("repo config populates RepoDesired with folder ID and share list", func(t *testing.T) {
		t.Parallel()
		machine := &config.MachineConfigV2{
			Name:             "desktop",
			DefaultShareWith: []string{"elitebook"},
		}
		repos := map[string]*config.RepoConfigV2{
			"/home/user/dotfiles": {
				Sync: config.RepoSyncConfig{
					SyncthingFolderID: "dk-dotfiles",
					ShareWith:         []string{"elitebook", "server"},
					Ignore:            []string{"*.log"},
				},
			},
		}
		d := BuildDesired(machine, repos, nil)
		r, ok := d.Repos["/home/user/dotfiles"]
		if !ok {
			t.Fatal("expected /home/user/dotfiles in Repos")
		}
		if r.SyncthingFolderID != "dk-dotfiles" {
			t.Errorf("wrong folder ID: %q", r.SyncthingFolderID)
		}
		if len(r.ShareWith) != 2 {
			t.Errorf("expected 2 share_with entries, got %d", len(r.ShareWith))
		}
		if len(r.Ignore) != 1 || r.Ignore[0] != "*.log" {
			t.Errorf("wrong ignore list: %v", r.Ignore)
		}
	})

	t.Run("repo with empty share_with inherits machine DefaultShareWith", func(t *testing.T) {
		t.Parallel()
		machine := &config.MachineConfigV2{
			DefaultShareWith: []string{"server"},
		}
		repos := map[string]*config.RepoConfigV2{
			"/repo": {
				Sync: config.RepoSyncConfig{
					SyncthingFolderID: "dk-repo",
					ShareWith:         []string{},
				},
			},
		}
		d := BuildDesired(machine, repos, nil)
		r := d.Repos["/repo"]
		if len(r.ShareWith) != 1 || r.ShareWith[0] != "server" {
			t.Errorf("expected inherited share_with [server], got %v", r.ShareWith)
		}
	})

	t.Run("nil repo entry in map is skipped", func(t *testing.T) {
		t.Parallel()
		repos := map[string]*config.RepoConfigV2{
			"/good": {Sync: config.RepoSyncConfig{SyncthingFolderID: "dk-good"}},
			"/nil":  nil,
		}
		d := BuildDesired(nil, repos, nil)
		if _, ok := d.Repos["/nil"]; ok {
			t.Error("nil repo entry should be skipped")
		}
		if _, ok := d.Repos["/good"]; !ok {
			t.Error("non-nil repo entry should be present")
		}
	})
}

// TestBuildDesired_PeersFromState verifies that BuildDesired populates
// Desired.Peers from state.Peers (Name + DeviceID), not from DefaultShareWith.
func TestBuildDesired_PeersFromState(t *testing.T) {
	t.Parallel()

	machine := &config.MachineConfigV2{
		Name:             "desktop",
		DefaultShareWith: []string{"elitebook", "server"},
	}
	state := &config.StateV2{
		Peers: []config.PeerEntry{
			{Name: "elitebook", DeviceID: "ABC123"},
			{Name: "server", DeviceID: "DEF456"},
		},
	}
	d := BuildDesired(machine, nil, state)
	if len(d.Peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(d.Peers))
	}
	if d.Peers[0].Name != "elitebook" || d.Peers[0].DeviceID != "ABC123" {
		t.Errorf("peer[0]: expected {elitebook, ABC123}, got %+v", d.Peers[0])
	}
	if d.Peers[1].Name != "server" || d.Peers[1].DeviceID != "DEF456" {
		t.Errorf("peer[1]: expected {server, DEF456}, got %+v", d.Peers[1])
	}
}

// TestBuildDesired_NilStateNoPeers verifies that a nil state produces no peers.
func TestBuildDesired_NilStateNoPeers(t *testing.T) {
	t.Parallel()

	machine := &config.MachineConfigV2{
		Name:             "desktop",
		DefaultShareWith: []string{"elitebook"},
	}
	d := BuildDesired(machine, nil, nil)
	if len(d.Peers) != 0 {
		t.Errorf("expected 0 peers with nil state, got %d: %v", len(d.Peers), d.Peers)
	}
}

// TestBuildDesired_PeersIgnoreDefaultShareWith verifies that DefaultShareWith
// does NOT inflate the peer roster when state.Peers is empty.
func TestBuildDesired_PeersIgnoreDefaultShareWith(t *testing.T) {
	t.Parallel()

	machine := &config.MachineConfigV2{
		Name:             "desktop",
		DefaultShareWith: []string{"foo"},
	}
	state := &config.StateV2{
		Peers: []config.PeerEntry{},
	}
	d := BuildDesired(machine, nil, state)
	if len(d.Peers) != 0 {
		t.Errorf("expected 0 peers, DefaultShareWith must not inflate roster; got %d: %v", len(d.Peers), d.Peers)
	}
}

// TestBuildDesired_NoShareWithAliasing verifies that mutating one repo's
// ShareWith slice does not corrupt another repo's slice or the source config.
func TestBuildDesired_NoShareWithAliasing(t *testing.T) {
	t.Parallel()

	machine := &config.MachineConfigV2{
		DefaultShareWith: []string{"a", "b"},
	}
	repos := map[string]*config.RepoConfigV2{
		"/repo1": {Sync: config.RepoSyncConfig{SyncthingFolderID: "dk-r1"}},
		"/repo2": {Sync: config.RepoSyncConfig{SyncthingFolderID: "dk-r2"}},
	}
	d := BuildDesired(machine, repos, nil)

	// Mutate repo1's ShareWith in-place.
	sw1 := d.Repos["/repo1"].ShareWith
	sw1[0] = "x"

	// repo2 must be unaffected.
	sw2 := d.Repos["/repo2"].ShareWith
	if sw2[0] != "a" {
		t.Errorf("slice aliasing: mutating repo1.ShareWith[0] corrupted repo2.ShareWith[0] = %q (want \"a\")", sw2[0])
	}
	// Source config must be unaffected.
	if machine.DefaultShareWith[0] != "a" {
		t.Errorf("slice aliasing: mutating repo1.ShareWith[0] corrupted machine.DefaultShareWith[0] = %q (want \"a\")", machine.DefaultShareWith[0])
	}
}

// TestBuildDesired_NoIgnoreAliasing verifies that mutating a RepoDesired.Ignore
// slice does not corrupt the source RepoConfigV2.Sync.Ignore.
func TestBuildDesired_NoIgnoreAliasing(t *testing.T) {
	t.Parallel()

	src := &config.RepoConfigV2{
		Sync: config.RepoSyncConfig{
			SyncthingFolderID: "dk-test",
			Ignore:            []string{"pat"},
		},
	}
	repos := map[string]*config.RepoConfigV2{"/repo": src}
	d := BuildDesired(nil, repos, nil)

	// Mutate the resulting Ignore slice in-place.
	ig := d.Repos["/repo"].Ignore
	ig[0] = "x"

	// Source config must be unaffected.
	if src.Sync.Ignore[0] != "pat" {
		t.Errorf("ignore slice aliasing: mutating RepoDesired.Ignore[0] corrupted source Ignore[0] = %q (want \"pat\")", src.Sync.Ignore[0])
	}
}

// TestDiffIdempotentFolders verifies that calling Diff twice on the same
// inputs produces identical plans (pure function property).
func TestDiffIdempotentFolders(t *testing.T) {
	t.Parallel()

	desired := Desired{
		Repos: map[string]RepoDesired{
			"/a": {Path: "/a", SyncthingFolderID: "dk-a", ShareWith: []string{"DEV-1"}},
		},
	}
	obs := Observed{
		ManagedFolders: []FolderObs{
			{SyncthingFolderID: "dk-stale", Path: "/stale"},
		},
	}

	plan1 := Diff(desired, obs)
	plan2 := Diff(desired, obs)

	if len(plan1) != len(plan2) {
		t.Fatalf("plan lengths differ: %d vs %d", len(plan1), len(plan2))
	}
	for i := range plan1 {
		if plan1[i].Describe() != plan2[i].Describe() {
			t.Errorf("action[%d] differs: %q vs %q", i, plan1[i].Describe(), plan2[i].Describe())
		}
	}
}

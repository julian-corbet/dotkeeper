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
			name: "observed folder not in desired → RemoveSyncthingFolder",
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
			name: "dirty repo → GitCommitDirty",
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
			name: "repo with head commit → GitPushRepo",
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
			name: "dirty repo with head commit → commit then push",
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
			name: "clean repo with empty head commit → no git actions",
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
			name: "multiple repos produce actions in path-sorted order",
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
		d := BuildDesired(nil, nil)
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

	t.Run("machine name and default_share_with populate Desired", func(t *testing.T) {
		t.Parallel()
		machine := &config.MachineConfigV2{
			Name:             "elitebook",
			DefaultShareWith: []string{"desktop", "server"},
		}
		d := BuildDesired(machine, nil)
		if d.MachineName != "elitebook" {
			t.Errorf("expected MachineName=elitebook, got %q", d.MachineName)
		}
		if len(d.Peers) != 2 {
			t.Fatalf("expected 2 peers, got %d", len(d.Peers))
		}
		if d.Peers[0].Name != "desktop" || d.Peers[1].Name != "server" {
			t.Errorf("unexpected peers: %v", d.Peers)
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
		d := BuildDesired(machine, repos)
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
		d := BuildDesired(machine, repos)
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
		d := BuildDesired(nil, repos)
		if _, ok := d.Repos["/nil"]; ok {
			t.Error("nil repo entry should be skipped")
		}
		if _, ok := d.Repos["/good"]; !ok {
			t.Error("non-nil repo entry should be present")
		}
	})
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

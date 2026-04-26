// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeMachineToml writes a minimal machine.toml into dir and returns its path.
func writeMachineToml(t *testing.T, dir string, scanRoots []string, exclude []string, scanDepth int) string {
	t.Helper()

	roots := ""
	for _, r := range scanRoots {
		roots += fmt.Sprintf("  %q,\n", r)
	}
	excl := ""
	for _, e := range exclude {
		excl += fmt.Sprintf("  %q,\n", e)
	}

	content := fmt.Sprintf(`schema_version = 2
name = "testmachine"
slot = 0
default_commit_policy = "manual"
default_git_interval = "hourly"
default_slot_offset_minutes = 5
reconcile_interval = "5m"
default_share_with = []

[discovery]
scan_roots = [
%s]
exclude = [
%s]
scan_interval = "5m"
scan_depth = %d
`, roots, excl, scanDepth)

	path := filepath.Join(dir, "machine.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing machine.toml: %v", err)
	}
	return path
}

// writeDotkeeperToml writes a minimal dotkeeper.toml into repoDir.
func writeDotkeeperToml(t *testing.T, repoDir, folderID string) {
	t.Helper()
	content := fmt.Sprintf(`schema_version = 2

[repo]
name = %q
added = "2026-01-01"
added_by = "testmachine"

[sync]
syncthing_folder_id = %q
ignore = []
share_with = []

[commit]
policy = ""
idle_seconds = 0

[git_backup]
interval = ""
skip_slots = []
`, filepath.Base(repoDir), folderID)

	path := filepath.Join(repoDir, "dotkeeper.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing dotkeeper.toml in %s: %v", repoDir, err)
	}
}

// writeStateToml writes a minimal state.toml at path, optionally including
// tracked override paths.
func writeStateToml(t *testing.T, path string, trackedOverrides []string) {
	t.Helper()

	overrides := ""
	for _, p := range trackedOverrides {
		overrides += fmt.Sprintf("  %q,\n", p)
	}

	content := fmt.Sprintf(`schema_version = 2
syncthing_device_id = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
tracked_overrides = [
%s]
`, overrides)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing state.toml at %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// DesiredProvider tests
// ---------------------------------------------------------------------------

func TestDesiredProvider_MissingMachineToml(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	provider := NewDesiredProvider(filepath.Join(dir, "machine.toml"))

	_, err := provider(context.Background())
	if err == nil {
		t.Fatal("expected error for missing machine.toml, got nil")
	}
	// Error message should mention dotkeeper init.
	if !containsSubstr(err.Error(), "dotkeeper init") {
		t.Errorf("error should mention 'dotkeeper init', got: %v", err)
	}
}

func TestDesiredProvider_EmptyScanRoots(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Write a machine.toml that points at a scan root that does not exist.
	nonExistentRoot := filepath.Join(dir, "repos")
	machineFile := writeMachineToml(t, dir, []string{nonExistentRoot}, nil, 3)

	provider := NewDesiredProvider(machineFile)
	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(desired.Repos) != 0 {
		t.Errorf("expected 0 repos for nonexistent root, got %d", len(desired.Repos))
	}
}

func TestDesiredProvider_DiscoversSingleRepo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place one repo directly under scanRoot.
	repoDir := filepath.Join(scanRoot, "myrepo")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDotkeeperToml(t, repoDir, "dk-myrepo")

	machineFile := writeMachineToml(t, dir, []string{scanRoot}, nil, 3)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(desired.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d: %v", len(desired.Repos), desired.Repos)
	}
	rd, ok := desired.Repos[repoDir]
	if !ok {
		t.Fatalf("expected repo at %s", repoDir)
	}
	if rd.SyncthingFolderID != "dk-myrepo" {
		t.Errorf("wrong folder ID: %q", rd.SyncthingFolderID)
	}
}

func TestDesiredProvider_DiscoversMultipleRepos(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		repoDir := filepath.Join(scanRoot, name)
		if err := os.Mkdir(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDotkeeperToml(t, repoDir, "dk-"+name)
	}

	machineFile := writeMachineToml(t, dir, []string{scanRoot}, nil, 3)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(desired.Repos) != 3 {
		t.Errorf("expected 3 repos, got %d", len(desired.Repos))
	}
}

func TestDesiredProvider_ExcludedDirSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	included := filepath.Join(scanRoot, "included")
	excluded := filepath.Join(scanRoot, "excluded")
	for _, d := range []string{included, excluded} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDotkeeperToml(t, d, "dk-"+filepath.Base(d))
	}

	machineFile := writeMachineToml(t, dir, []string{scanRoot}, []string{excluded}, 3)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(desired.Repos) != 1 {
		t.Fatalf("expected 1 repo (excluded dir skipped), got %d: %v", len(desired.Repos), desired.Repos)
	}
	if _, ok := desired.Repos[excluded]; ok {
		t.Error("excluded dir should not be in repos")
	}
	if _, ok := desired.Repos[included]; !ok {
		t.Error("included dir should be in repos")
	}
}

func TestDesiredProvider_ScanDepthRespected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a repo at depth 1 (should be found).
	shallow := filepath.Join(scanRoot, "shallow")
	if err := os.MkdirAll(shallow, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDotkeeperToml(t, shallow, "dk-shallow")

	// Create a repo at depth 3 (beyond the configured limit of 2).
	deep := filepath.Join(scanRoot, "a", "b", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDotkeeperToml(t, deep, "dk-deep")

	machineFile := writeMachineToml(t, dir, []string{scanRoot}, nil, 2)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := desired.Repos[shallow]; !ok {
		t.Error("shallow repo (depth=1) should be discovered")
	}
	if _, ok := desired.Repos[deep]; ok {
		t.Error("deep repo (depth=3) should NOT be discovered with scan_depth=2")
	}
}

func TestDesiredProvider_RepoAtScanRootItself(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place dotkeeper.toml directly in the scan root.
	writeDotkeeperToml(t, scanRoot, "dk-root")

	machineFile := writeMachineToml(t, dir, []string{scanRoot}, nil, 3)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := desired.Repos[scanRoot]; !ok {
		t.Error("repo at scan root itself should be discovered")
	}
}

func TestDesiredProvider_NestedRepoBeyondDepthNotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// depth=1 (one level under scanRoot)
	level1 := filepath.Join(scanRoot, "org")
	if err := os.MkdirAll(level1, 0o755); err != nil {
		t.Fatal(err)
	}
	// depth=2 (two levels under scanRoot) — within scan_depth=1 limit? No, depth=1 means
	// we walk one level, so level1 is already at depth=1 (found), level2 would be depth=2.
	level2 := filepath.Join(level1, "project")
	if err := os.MkdirAll(level2, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDotkeeperToml(t, level2, "dk-project")

	// With scan_depth=1 only depth-1 dirs are visited.
	machineFile := writeMachineToml(t, dir, []string{scanRoot}, nil, 1)
	provider := NewDesiredProvider(machineFile)

	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := desired.Repos[level2]; ok {
		t.Error("repo at depth=2 should NOT be discovered with scan_depth=1")
	}
}

func TestDesiredProvider_MachineName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	machineFile := writeMachineToml(t, dir, []string{}, nil, 3)

	provider := NewDesiredProvider(machineFile)
	desired, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desired.MachineName != "testmachine" {
		t.Errorf("expected MachineName=testmachine, got %q", desired.MachineName)
	}
}

// ---------------------------------------------------------------------------
// ObservedProvider tests — stub Syncthing client
// ---------------------------------------------------------------------------

// stubQuerier implements SyncthingQuerier for unit tests.
type stubQuerier struct {
	cfg   map[string]any
	conns *stclient.Connections
	err   error
}

func (s *stubQuerier) GetConfig() (map[string]any, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func (s *stubQuerier) GetConnections() (*stclient.Connections, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.conns, nil
}

func TestObservedProvider_NilClient(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	provider := newObservedProvider(nil, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error with nil client: %v", err)
	}
	if len(obs.ManagedFolders) != 0 {
		t.Errorf("expected 0 ManagedFolders, got %d", len(obs.ManagedFolders))
	}
	if len(obs.LivePeers) != 0 {
		t.Errorf("expected 0 LivePeers, got %d", len(obs.LivePeers))
	}
}

func TestObservedProvider_SyncthingUnreachable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	q := &stubQuerier{err: fmt.Errorf("connection refused")}
	provider := newObservedProvider(q, statePath)

	_, err := provider(context.Background())
	if err == nil {
		t.Fatal("expected error when Syncthing is unreachable, got nil")
	}
	if !containsSubstr(err.Error(), "Syncthing") {
		t.Errorf("error should mention 'Syncthing', got: %v", err)
	}
}

func TestObservedProvider_FoldersAndPeers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	q := &stubQuerier{
		cfg: map[string]any{
			"folders": []any{
				map[string]any{
					"id":   "dk-dotfiles",
					"path": "/home/user/dotfiles",
					"devices": []any{
						map[string]any{"deviceID": "DEVICE-A"},
						map[string]any{"deviceID": "DEVICE-B"},
					},
				},
				map[string]any{
					"id":      "dk-notes",
					"path":    "/home/user/notes",
					"devices": []any{},
				},
			},
		},
		conns: &stclient.Connections{
			Connections: map[string]stclient.Connection{
				"DEVICE-A": {Connected: true, Address: "tcp://10.0.0.1"},
				"DEVICE-B": {Connected: false},
			},
		},
	}

	provider := newObservedProvider(q, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(obs.ManagedFolders) != 2 {
		t.Fatalf("expected 2 ManagedFolders, got %d", len(obs.ManagedFolders))
	}

	// Find the dotfiles folder and check devices.
	var dotfolder *FolderObs
	for i := range obs.ManagedFolders {
		if obs.ManagedFolders[i].SyncthingFolderID == "dk-dotfiles" {
			dotfolder = &obs.ManagedFolders[i]
			break
		}
	}
	if dotfolder == nil {
		t.Fatal("dk-dotfiles folder not found in ManagedFolders")
	}
	if len(dotfolder.Devices) != 2 {
		t.Errorf("expected 2 devices for dk-dotfiles, got %d", len(dotfolder.Devices))
	}
	if dotfolder.Path != "/home/user/dotfiles" {
		t.Errorf("wrong path: %q", dotfolder.Path)
	}

	// Two peers should appear in LivePeers.
	if len(obs.LivePeers) != 2 {
		t.Fatalf("expected 2 LivePeers, got %d", len(obs.LivePeers))
	}

	// Verify connected flag is propagated.
	peersConnected := 0
	for _, p := range obs.LivePeers {
		if p.Connected {
			peersConnected++
		}
		if p.DeviceID == "" {
			t.Error("LivePeer has empty DeviceID")
		}
	}
	if peersConnected != 1 {
		t.Errorf("expected 1 connected peer, got %d", peersConnected)
	}
}

func TestObservedProvider_FolderWithoutID_Skipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	q := &stubQuerier{
		cfg: map[string]any{
			"folders": []any{
				// Folder with no id field — should be skipped.
				map[string]any{
					"path":    "/some/path",
					"devices": []any{},
				},
				// Valid folder.
				map[string]any{
					"id":      "dk-valid",
					"path":    "/valid",
					"devices": []any{},
				},
			},
		},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{}},
	}

	provider := newObservedProvider(q, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.ManagedFolders) != 1 {
		t.Errorf("expected 1 valid folder, got %d", len(obs.ManagedFolders))
	}
}

func TestObservedProvider_MissingStateToml(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	q := &stubQuerier{
		cfg:   map[string]any{"folders": []any{}},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{}},
	}

	provider := newObservedProvider(q, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error for missing state.toml: %v", err)
	}
	if obs.CachedState != nil {
		t.Error("CachedState should be nil when state.toml does not exist")
	}
	if len(obs.TrackedRepos) != 0 {
		t.Errorf("expected 0 TrackedRepos, got %d", len(obs.TrackedRepos))
	}
}

func TestObservedProvider_TrackedOverridesLoadedFromState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	// Use a repo path that exists but is not a git repo — HeadCommit will be
	// empty and IsDirty false; what we're testing is that the path appears in
	// TrackedRepos.
	fakeRepo := filepath.Join(dir, "fakerepo")
	if err := os.Mkdir(fakeRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	writeStateToml(t, statePath, []string{fakeRepo})

	q := &stubQuerier{
		cfg:   map[string]any{"folders": []any{}},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{}},
	}

	provider := newObservedProvider(q, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(obs.TrackedRepos) != 1 {
		t.Fatalf("expected 1 TrackedRepo, got %d", len(obs.TrackedRepos))
	}
	if obs.TrackedRepos[0].Path != fakeRepo {
		t.Errorf("expected path %s, got %s", fakeRepo, obs.TrackedRepos[0].Path)
	}
}

func TestObservedProvider_CachedStateLastBackupAt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.toml")

	fakeRepo := filepath.Join(dir, "repo")
	if err := os.Mkdir(fakeRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a state.toml with an observed_repos entry for fakeRepo.
	backupTime := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	content := fmt.Sprintf(`schema_version = 2
syncthing_device_id = "AAAAAAA-BBBBBBB"
tracked_overrides = [
  %q,
]

[observed_repos.%q]
last_reconciled_commit = "abc123"
last_pushed_commit = "abc123"
last_backup_at = %s
`, fakeRepo, fakeRepo, backupTime.Format(time.RFC3339))

	if err := os.WriteFile(statePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	q := &stubQuerier{
		cfg:   map[string]any{"folders": []any{}},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{}},
	}

	provider := newObservedProvider(q, statePath)
	obs, err := provider(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(obs.TrackedRepos) != 1 {
		t.Fatalf("expected 1 TrackedRepo, got %d", len(obs.TrackedRepos))
	}
	if obs.TrackedRepos[0].LastBackupAt.IsZero() {
		t.Error("LastBackupAt should be populated from state.toml")
	}
}

// ---------------------------------------------------------------------------
// Unit tests for internal helpers
// ---------------------------------------------------------------------------

func TestExpandTilde(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()

	tests := []struct {
		input    string
		wantSufx string
	}{
		{"~/foo", home + "/foo"},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~/", home + "/"},
	}
	for _, tc := range tests {
		got, err := expandTilde(tc.input)
		if err != nil {
			t.Errorf("expandTilde(%q) error: %v", tc.input, err)
			continue
		}
		if got != filepath.Clean(tc.wantSufx) && got != tc.wantSufx {
			// Allow for filepath.Clean differences on the home suffix edge case.
			if tc.input != "~/" {
				t.Errorf("expandTilde(%q) = %q, want prefix of %q", tc.input, got, tc.wantSufx)
			}
		}
	}
}

func TestWalkScanRoot_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repos := make(map[string]*config.RepoConfigV2)
	if err := walkScanRoot(dir, nil, 3, repos); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected 0 repos in empty dir, got %d", len(repos))
	}
}

func TestWalkScanRoot_NonexistentDir(t *testing.T) {
	t.Parallel()

	repos := make(map[string]*config.RepoConfigV2)
	// Non-existent directory should produce no error (os.ReadDir error is silenced).
	if err := walkScanRoot("/does/not/exist/ever", nil, 3, repos); err != nil {
		t.Fatalf("unexpected error for nonexistent dir: %v", err)
	}
}

func TestQueryRepoGitState_NotARepo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	obs := queryRepoGitState(dir, nil)

	if obs.Path != dir {
		t.Errorf("expected Path=%s, got %s", dir, obs.Path)
	}
	// Not a git repo: HeadCommit and IsDirty should be zero values.
	if obs.HeadCommit != "" {
		t.Errorf("expected empty HeadCommit for non-repo, got %q", obs.HeadCommit)
	}
	if obs.IsDirty {
		t.Error("expected IsDirty=false for non-repo")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}


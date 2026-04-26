// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// --- Fake Syncthing client ---------------------------------------------------

// fakeST is a test double for SyncthingClient. It stores a mutable config map
// and a fixed device ID. Any method can be made to return an error by setting
// the corresponding Err* field.
type fakeST struct {
	cfg       map[string]any
	myID      string
	ErrGet    error
	ErrSet    error
	ErrAdd    error
	ErrStatus error
}

func newFakeST() *fakeST {
	return &fakeST{
		cfg: map[string]any{
			"devices": []any{},
			"folders": []any{},
		},
		myID: "MY-OWN-DEVICE-ID",
	}
}

func (f *fakeST) GetConfig() (map[string]any, error) {
	if f.ErrGet != nil {
		return nil, f.ErrGet
	}
	// Return a deep copy so the caller can modify it freely.
	out := make(map[string]any, len(f.cfg))
	for k, v := range f.cfg {
		out[k] = v
	}
	return out, nil
}

func (f *fakeST) SetConfig(cfg map[string]any) error {
	if f.ErrSet != nil {
		return f.ErrSet
	}
	f.cfg = cfg
	return nil
}

func (f *fakeST) GetStatus() (*stclient.SystemStatus, error) {
	if f.ErrStatus != nil {
		return nil, f.ErrStatus
	}
	return &stclient.SystemStatus{MyID: f.myID}, nil
}

func (f *fakeST) AddOrUpdateFolder(id, label, path string, deviceIDs []string) error {
	if f.ErrAdd != nil {
		return f.ErrAdd
	}
	folders, _ := f.cfg["folders"].([]any)
	for i, fl := range folders {
		fm, _ := fl.(map[string]any)
		if fm["id"] == id {
			fm["label"] = label
			fm["path"] = path
			folders[i] = fm
			f.cfg["folders"] = folders
			return nil
		}
	}
	f.cfg["folders"] = append(folders, map[string]any{
		"id":    id,
		"label": label,
		"path":  path,
	})
	return nil
}

// seedFolders sets the fake's folder list.
func (f *fakeST) seedFolders(folders []map[string]any) {
	out := make([]any, len(folders))
	for i, m := range folders {
		out[i] = m
	}
	f.cfg["folders"] = out
}

// folders returns the current folder list from the fake's config.
func (f *fakeST) folders() []map[string]any {
	raw, _ := f.cfg["folders"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// --- Git test helpers --------------------------------------------------------

var gitTestEnv = []string{
	"GIT_AUTHOR_NAME=testbot",
	"GIT_AUTHOR_EMAIL=testbot@example.com",
	"GIT_COMMITTER_NAME=testbot",
	"GIT_COMMITTER_EMAIL=testbot@example.com",
}

// setupBareAndClone creates a bare git remote and a working clone inside
// t.TempDir(). Returns the clone path.
func setupBareAndClone(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), gitTestEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work)
	run(work, "git", "checkout", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# test\n"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-m", "initial")
	run(work, "git", "push", "-u", "origin", "main")
	return work
}

// runGit runs a git command in dir, returning combined output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitTestEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// --- AddSyncthingFolder tests ------------------------------------------------

func TestRealApplierAddSyncthingFolder(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	applier := &RealApplier{ST: fake}

	act := AddSyncthingFolder{
		FolderID: "dk-dotfiles",
		Path:     "/home/user/dotfiles",
		Devices:  []string{"PEER-X"},
	}
	if err := applier.Apply(context.Background(), act); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	folders := fake.folders()
	if len(folders) != 1 {
		t.Fatalf("expected 1 folder, got %d", len(folders))
	}
	if folders[0]["id"] != "dk-dotfiles" {
		t.Errorf("id = %v", folders[0]["id"])
	}
	if folders[0]["path"] != "/home/user/dotfiles" {
		t.Errorf("path = %v", folders[0]["path"])
	}
}

func TestRealApplierAddSyncthingFolderError(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.ErrAdd = errors.New("syncthing unavailable")
	applier := &RealApplier{ST: fake}

	err := applier.Apply(context.Background(), AddSyncthingFolder{FolderID: "x", Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "syncthing unavailable") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- RemoveSyncthingFolder tests ---------------------------------------------

func TestRealApplierRemoveSyncthingFolder(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.seedFolders([]map[string]any{
		{"id": "dk-keep", "path": "/keep"},
		{"id": "dk-remove", "path": "/remove"},
	})
	applier := &RealApplier{ST: fake}

	if err := applier.Apply(context.Background(), RemoveSyncthingFolder{FolderID: "dk-remove"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	folders := fake.folders()
	if len(folders) != 1 {
		t.Fatalf("expected 1 folder remaining, got %d", len(folders))
	}
	if folders[0]["id"] != "dk-keep" {
		t.Errorf("wrong folder remains: %v", folders[0]["id"])
	}
}

func TestRealApplierRemoveSyncthingFolderMissing(t *testing.T) {
	t.Parallel()

	// Folder not in config — should be a no-op, not an error.
	fake := newFakeST()
	applier := &RealApplier{ST: fake}

	if err := applier.Apply(context.Background(), RemoveSyncthingFolder{FolderID: "nonexistent"}); err != nil {
		t.Fatalf("expected no-op for missing folder, got: %v", err)
	}
	if len(fake.folders()) != 0 {
		t.Errorf("folders should still be empty")
	}
}

func TestRealApplierRemoveSyncthingFolderGetError(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.ErrGet = errors.New("network error")
	applier := &RealApplier{ST: fake}

	err := applier.Apply(context.Background(), RemoveSyncthingFolder{FolderID: "x"})
	if err == nil {
		t.Fatal("expected error when GetConfig fails")
	}
}

func TestRealApplierRemoveSyncthingFolderSetError(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.seedFolders([]map[string]any{{"id": "dk-x", "path": "/x"}})
	fake.ErrSet = errors.New("write failed")
	applier := &RealApplier{ST: fake}

	err := applier.Apply(context.Background(), RemoveSyncthingFolder{FolderID: "dk-x"})
	if err == nil {
		t.Fatal("expected error when SetConfig fails")
	}
}

// --- UpdateSyncthingFolderDevices tests --------------------------------------

func TestRealApplierUpdateSyncthingFolderDevices(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.seedFolders([]map[string]any{
		{
			"id":      "dk-dotfiles",
			"path":    "/dotfiles",
			"devices": []any{map[string]any{"deviceID": "OLD-PEER"}},
		},
	})
	applier := &RealApplier{ST: fake}

	act := UpdateSyncthingFolderDevices{
		FolderID: "dk-dotfiles",
		Devices:  []string{"NEW-PEER-A", "NEW-PEER-B"},
	}
	if err := applier.Apply(context.Background(), act); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	folders := fake.folders()
	rawDevs, _ := folders[0]["devices"].([]map[string]any)
	if len(rawDevs) != 2 {
		t.Errorf("expected 2 devices, got %d", len(rawDevs))
	}
}

func TestRealApplierUpdateSyncthingFolderDevicesExcludesOwnID(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.seedFolders([]map[string]any{
		{"id": "dk-dotfiles", "path": "/dotfiles"},
	})
	applier := &RealApplier{ST: fake}

	// Include own device ID in requested devices — it must be excluded.
	act := UpdateSyncthingFolderDevices{
		FolderID: "dk-dotfiles",
		Devices:  []string{fake.myID, "PEER-X"},
	}
	if err := applier.Apply(context.Background(), act); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	folders := fake.folders()
	rawDevs, _ := folders[0]["devices"].([]map[string]any)
	for _, d := range rawDevs {
		if d["deviceID"] == fake.myID {
			t.Error("own device ID must not appear in folder devices")
		}
	}
}

func TestRealApplierUpdateSyncthingFolderDevicesNotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	applier := &RealApplier{ST: fake}

	err := applier.Apply(context.Background(), UpdateSyncthingFolderDevices{
		FolderID: "nonexistent",
		Devices:  []string{"PEER"},
	})
	if err == nil {
		t.Fatal("expected error when folder not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestRealApplierUpdateSyncthingFolderDevicesGetStatusError(t *testing.T) {
	t.Parallel()

	fake := newFakeST()
	fake.ErrStatus = errors.New("status unavailable")
	applier := &RealApplier{ST: fake}

	err := applier.Apply(context.Background(), UpdateSyncthingFolderDevices{
		FolderID: "dk-x",
		Devices:  []string{"P"},
	})
	if err == nil {
		t.Fatal("expected error when GetStatus fails")
	}
}

// --- GitCommitDirty tests ----------------------------------------------------

func TestRealApplierGitCommitDirty(t *testing.T) {
	t.Parallel()

	work := setupBareAndClone(t)
	_ = os.WriteFile(filepath.Join(work, "dirty.txt"), []byte("new content\n"), 0o644)

	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), GitCommitDirty{
		RepoPath: work,
		Message:  "auto: test commit",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out := runGit(t, work, "log", "--oneline", "-1")
	if !strings.Contains(out, "auto: test commit") {
		t.Errorf("commit message not found: %q", out)
	}
}

func TestRealApplierGitCommitDirtyIdempotentOnCleanRepo(t *testing.T) {
	t.Parallel()

	work := setupBareAndClone(t)

	// Clean repo — Apply must be a no-op.
	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), GitCommitDirty{
		RepoPath: work,
		Message:  "auto: should not appear",
	}); err != nil {
		t.Fatalf("Apply on clean repo: %v", err)
	}

	count := runGit(t, work, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Errorf("expected 1 commit, got %q", count)
	}
}

func TestRealApplierGitCommitDirtyNotARepo(t *testing.T) {
	t.Parallel()

	applier := &RealApplier{}
	err := applier.Apply(context.Background(), GitCommitDirty{
		RepoPath: t.TempDir(),
		Message:  "auto: fail",
	})
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestRealApplierGitCommitDirtyRespectsGitEnv(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	work := setupBareAndClone(t)
	_ = os.WriteFile(filepath.Join(work, "env-test.txt"), []byte("data\n"), 0o644)

	// Override identity via env — gitRun propagates os.Environ() which includes
	// values set by t.Setenv.
	t.Setenv("GIT_AUTHOR_NAME", "EnvBot")
	t.Setenv("GIT_AUTHOR_EMAIL", "envbot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "EnvBot")
	t.Setenv("GIT_COMMITTER_EMAIL", "envbot@example.com")

	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), GitCommitDirty{
		RepoPath: work,
		Message:  "auto: env identity test",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%an")
	cmd.Dir = work
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "EnvBot" {
		t.Errorf("expected author 'EnvBot', got %q", strings.TrimSpace(string(out)))
	}
}

// --- GitPushRepo tests -------------------------------------------------------

func TestRealApplierGitPushRepo(t *testing.T) {
	t.Parallel()

	work := setupBareAndClone(t)

	// Create a local commit that hasn't been pushed.
	_ = os.WriteFile(filepath.Join(work, "push-me.txt"), []byte("push me\n"), 0o644)
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "local commit")

	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), GitPushRepo{RepoPath: work}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify the remote has the commit.
	out := runGit(t, work, "log", "--oneline", "origin/main", "-1")
	if !strings.Contains(out, "local commit") {
		t.Errorf("push did not reach remote: %q", out)
	}
}

func TestRealApplierGitPushRepoBrokenRemote(t *testing.T) {
	t.Parallel()

	work := setupBareAndClone(t)
	runGit(t, work, "remote", "set-url", "origin", "/nonexistent/path")

	applier := &RealApplier{}
	err := applier.Apply(context.Background(), GitPushRepo{RepoPath: work})
	if err == nil {
		t.Fatal("expected push error with broken remote")
	}
}

// --- TrackRepo tests ---------------------------------------------------------

func TestRealApplierTrackRepo(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), TrackRepo{Path: "/home/user/myrepo"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	state, err := loadOrInitState()
	if err != nil {
		t.Fatalf("loadOrInitState: %v", err)
	}
	found := false
	for _, p := range state.TrackedOverrides {
		if p == "/home/user/myrepo" {
			found = true
		}
	}
	if !found {
		t.Errorf("path not in TrackedOverrides: %v", state.TrackedOverrides)
	}
}

func TestRealApplierTrackRepoIdempotent(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	applier := &RealApplier{}
	act := TrackRepo{Path: "/home/user/myrepo"}

	// Apply twice — must not duplicate the entry.
	if err := applier.Apply(context.Background(), act); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := applier.Apply(context.Background(), act); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	state, _ := loadOrInitState()
	count := 0
	for _, p := range state.TrackedOverrides {
		if p == "/home/user/myrepo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 entry after 2 applies, got %d", count)
	}
}

func TestRealApplierTrackRepoWriteError(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	t.Setenv("XDG_STATE_HOME", "/nonexistent/readonly")

	applier := &RealApplier{}
	err := applier.Apply(context.Background(), TrackRepo{Path: "/some/path"})
	if err == nil {
		t.Fatal("expected error when state dir is unwritable")
	}
}

// --- UntrackRepo tests -------------------------------------------------------

func TestRealApplierUntrackRepo(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	applier := &RealApplier{}
	// Pre-populate with two paths.
	if err := applier.Apply(context.Background(), TrackRepo{Path: "/keep"}); err != nil {
		t.Fatalf("track /keep: %v", err)
	}
	if err := applier.Apply(context.Background(), TrackRepo{Path: "/remove"}); err != nil {
		t.Fatalf("track /remove: %v", err)
	}

	// Untrack one.
	if err := applier.Apply(context.Background(), UntrackRepo{Path: "/remove"}); err != nil {
		t.Fatalf("Apply UntrackRepo: %v", err)
	}

	state, _ := loadOrInitState()
	for _, p := range state.TrackedOverrides {
		if p == "/remove" {
			t.Error("/remove should have been removed")
		}
	}
	found := false
	for _, p := range state.TrackedOverrides {
		if p == "/keep" {
			found = true
		}
	}
	if !found {
		t.Error("/keep should remain in TrackedOverrides")
	}
}

func TestRealApplierUntrackRepoMissing(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	applier := &RealApplier{}
	// Path was never tracked — should be a no-op, not an error.
	if err := applier.Apply(context.Background(), UntrackRepo{Path: "/not/tracked"}); err != nil {
		t.Fatalf("expected no-op for untracked path, got: %v", err)
	}
}

func TestRealApplierUntrackRepoWriteError(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel in Go 1.26.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	// Seed the state so the path exists and the untrack would normally write.
	applier := &RealApplier{}
	if err := applier.Apply(context.Background(), TrackRepo{Path: "/to-remove"}); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Make the state file itself read-only to force a write error.
	// (Revoking write on the directory only prevents new file creation, not
	//  updates to existing files when the caller already holds the path.)
	statePath := filepath.Join(dir, "dotkeeper", "state.toml")
	if err := os.Chmod(statePath, 0o400); err != nil {
		t.Skipf("chmod not supported on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(statePath, 0o600) })

	err := applier.Apply(context.Background(), UntrackRepo{Path: "/to-remove"})
	if err == nil {
		t.Fatal("expected error when state file is not writable")
	}
}

// --- Unknown action type test ------------------------------------------------

// unknownAction is an Action type not handled by RealApplier.
type unknownAction struct{}

func (u unknownAction) Describe() string { return "unknown" }

func TestRealApplierUnknownAction(t *testing.T) {
	t.Parallel()

	applier := &RealApplier{}
	err := applier.Apply(context.Background(), unknownAction{})
	if err == nil {
		t.Fatal("expected error for unknown action type")
	}
	if !strings.Contains(err.Error(), "unknown action type") {
		t.Errorf("error should mention 'unknown action type', got: %v", err)
	}
}

// --- httptest smoke test (verifies SyncthingClient interface compliance) -----

// TestRealSTClientSatisfiesInterface is a compile-time check: if *stclient.Client
// does not satisfy SyncthingClient the test binary will not build.
// We exercise it with a real httptest server to keep the test meaningful.
func TestRealSTClientSatisfiesInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond 200 to everything; we only care the types compile.
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var _ SyncthingClient = stclient.New("test-key")
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestWalkScanRootStopsAtRepoRoot asserts the canonical walk behaviour:
// a directory containing dotkeeper.toml is reported via fn and the walk does
// not descend into it. Any dotkeeper.toml files nested further inside should
// not be found as separate repos.
func TestWalkScanRootStopsAtRepoRoot(t *testing.T) {
	tmp := t.TempDir()

	// Structure:
	//   root/
	//     repo-a/
	//       dotkeeper.toml       <- managed repo root
	//       sub/
	//         dotkeeper.toml     <- must NOT be reported (descent stopped)
	//     repo-b/
	//       dotkeeper.toml       <- separate managed repo
	repoA := filepath.Join(tmp, "repo-a")
	repoASub := filepath.Join(repoA, "sub")
	repoB := filepath.Join(tmp, "repo-b")

	for _, d := range []string{repoA, repoASub, repoB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	for _, f := range []string{
		filepath.Join(repoA, "dotkeeper.toml"),
		filepath.Join(repoASub, "dotkeeper.toml"),
		filepath.Join(repoB, "dotkeeper.toml"),
	} {
		if err := os.WriteFile(f, []byte{}, 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	var found []string
	if err := WalkScanRoot(tmp, 0, 3, func(p string) { found = append(found, p) }); err != nil {
		t.Fatalf("WalkScanRoot: %v", err)
	}

	if len(found) != 2 {
		t.Errorf("expected 2 repos; got %d: %v", len(found), found)
	}

	// repo-a/sub must not appear.
	for _, p := range found {
		if p == repoASub {
			t.Errorf("walk descended into repo-a/ and reported nested sub/: %v", found)
		}
	}

	wantSet := map[string]bool{repoA: true, repoB: true}
	for _, p := range found {
		if !wantSet[p] {
			t.Errorf("unexpected path reported: %s", p)
		}
	}
}

// TestWalkScanRootRespectsMaxDepth asserts that repos deeper than maxDepth
// are not found.
func TestWalkScanRootRespectsMaxDepth(t *testing.T) {
	tmp := t.TempDir()

	// Structure:
	//   root/
	//     a/
	//       b/
	//         c/
	//           dotkeeper.toml   <- depth 3 from root
	deep := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "dotkeeper.toml"), []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var found []string
	// With maxDepth=2, depth 3 dirs should not be visited.
	_ = WalkScanRoot(tmp, 0, 2, func(p string) { found = append(found, p) })
	if len(found) != 0 {
		t.Errorf("expected 0 repos with maxDepth=2; got %d: %v", len(found), found)
	}

	// With maxDepth=3, it should be found.
	found = nil
	_ = WalkScanRoot(tmp, 0, 3, func(p string) { found = append(found, p) })
	if len(found) != 1 {
		t.Errorf("expected 1 repo with maxDepth=3; got %d: %v", len(found), found)
	}
}

// TestWalkScanRootSkipsHiddenDirs asserts that directories starting with "."
// are not descended into.
func TestWalkScanRootSkipsHiddenDirs(t *testing.T) {
	tmp := t.TempDir()

	hidden := filepath.Join(tmp, ".hidden-dir")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "dotkeeper.toml"), []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var found []string
	_ = WalkScanRoot(tmp, 0, 3, func(p string) { found = append(found, p) })
	if len(found) != 0 {
		t.Errorf("expected hidden dir to be skipped; got %d: %v", len(found), found)
	}
}

// TestManagedFolderPathsExcludesObservedReposWhenScanRootRemoved pins the
// documented behaviour from the ObservedRepos comment in ManagedFolderPaths:
// removing a scan_root from machine.toml must make repos under it invisible
// even if they are still recorded in state.toml's ObservedRepos.
//
// Setup:
//   - one scan_root pointing at a directory that contains a dotkeeper.toml repo
//   - that repo is also recorded in ObservedRepos in state.toml
//
// After removing the scan_root from machine.toml, ManagedFolderPaths should
// return 0 repo paths (other than the config dir itself, which is always
// included — we test the repo specifically is absent).
func TestManagedFolderPathsExcludesObservedReposWhenScanRootRemoved(t *testing.T) {
	tmp := t.TempDir()

	// Redirect XDG dirs into tmp so config/state reads hit our test data.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("HOME", tmp)

	// Create a scan_root with one managed repo inside it.
	scanRoot := filepath.Join(tmp, "repos")
	repoDir := filepath.Join(scanRoot, "my-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "dotkeeper.toml"), []byte{}, 0o600); err != nil {
		t.Fatalf("write dotkeeper.toml: %v", err)
	}

	// Write machine.toml pointing at the scan_root.
	cfgDir := filepath.Join(tmp, "config", "dotkeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	machineContent := fmt.Sprintf(`schema_version = 2
name = "test"
slot = 0
default_commit_policy = "manual"
default_git_interval = "hourly"
default_slot_offset_minutes = 5
reconcile_interval = "5m"
default_share_with = []

[discovery]
scan_roots = [%q]
exclude = []
scan_interval = "5m"
scan_depth = 3
`, scanRoot)
	if err := os.WriteFile(filepath.Join(cfgDir, "machine.toml"), []byte(machineContent), 0o600); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}

	// Write state.toml recording the repo in ObservedRepos.
	stateDir := filepath.Join(tmp, "state", "dotkeeper")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir stateDir: %v", err)
	}
	stateContent := fmt.Sprintf(`schema_version = 2
syncthing_device_id = "TESTDEV-ABCDEFG"
tracked_overrides = []

[observed_repos.%q]
last_reconciled_commit = "abc123"
last_pushed_commit = "abc123"
last_backup_at = 2026-01-01T00:00:00Z
`, repoDir)
	if err := os.WriteFile(filepath.Join(stateDir, "state.toml"), []byte(stateContent), 0o600); err != nil {
		t.Fatalf("write state.toml: %v", err)
	}

	// Confirm the repo IS found when the scan_root is present.
	withRoot := ManagedFolderPaths()
	found := false
	for _, p := range withRoot {
		if p == repoDir {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("precondition failed: repo should be found when scan_root is present; got %v", withRoot)
	}

	// Now remove the scan_root from machine.toml (empty scan_roots).
	machineNoRoot := `schema_version = 2
name = "test"
slot = 0
default_commit_policy = "manual"
default_git_interval = "hourly"
default_slot_offset_minutes = 5
reconcile_interval = "5m"
default_share_with = []

[discovery]
scan_roots = []
exclude = []
scan_interval = "5m"
scan_depth = 3
`
	if err := os.WriteFile(filepath.Join(cfgDir, "machine.toml"), []byte(machineNoRoot), 0o600); err != nil {
		t.Fatalf("update machine.toml: %v", err)
	}

	// ManagedFolderPaths must NOT return the repo even though it is still in
	// ObservedRepos — that is the exact behaviour the comment documents.
	withoutRoot := ManagedFolderPaths()
	for _, p := range withoutRoot {
		if p == repoDir {
			t.Errorf("repo %s appeared in ManagedFolderPaths despite scan_root removal; ObservedRepos must not be consulted", repoDir)
		}
	}
}

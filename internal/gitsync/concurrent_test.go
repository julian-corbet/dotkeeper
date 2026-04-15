// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentSyncSameRepo verifies that two concurrent SyncRepo calls
// on the same repo don't corrupt it. At least one should succeed.
// This simulates the real-world scenario where a cron job fires while
// the user is also manually syncing.
func TestConcurrentSyncSameRepo(t *testing.T) {
	work := setupGitRepo(t)

	// Create a file so there's something to commit
	os.WriteFile(filepath.Join(work, "concurrent.txt"), []byte("test\n"), 0o644)

	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = SyncRepo(work, "machine-"+string(rune('a'+idx)))
		}(i)
	}

	wg.Wait()

	// At least one must succeed
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Errorf("both concurrent syncs failed: %v, %v", errs[0], errs[1])
	}

	// Repo must not be corrupted — git status should work
	cmd := exec.Command("git", "status")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("repo corrupted after concurrent sync: %v\n%s", err, out)
	}

	// No rebase in progress
	if _, err := os.Stat(filepath.Join(work, ".git", "rebase-merge")); err == nil {
		t.Error("repo left in rebase state after concurrent sync")
	}
	if _, err := os.Stat(filepath.Join(work, ".git", "rebase-apply")); err == nil {
		t.Error("repo left in rebase-apply state after concurrent sync")
	}
}

// TestConcurrentSyncDifferentRepos verifies that syncing different repos
// in parallel doesn't interfere. Both should succeed.
func TestConcurrentSyncDifferentRepos(t *testing.T) {
	work1 := setupGitRepo(t)
	work2 := setupGitRepoNamed(t, "repo2")

	os.WriteFile(filepath.Join(work1, "file1.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(work2, "file2.txt"), []byte("two\n"), 0o644)

	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = SyncRepo(work1, "machine-a")
	}()
	go func() {
		defer wg.Done()
		errs[1] = SyncRepo(work2, "machine-a")
	}()

	wg.Wait()

	if errs[0] != nil {
		t.Errorf("repo1 sync failed: %v", errs[0])
	}
	if errs[1] != nil {
		t.Errorf("repo2 sync failed: %v", errs[1])
	}
}

// TestRapidSequentialSync verifies that syncing the same repo many times
// in rapid succession doesn't break anything.
func TestRapidSequentialSync(t *testing.T) {
	work := setupGitRepo(t)

	for i := 0; i < 5; i++ {
		// Alternate between changes and no-changes
		if i%2 == 0 {
			os.WriteFile(filepath.Join(work, "rapid.txt"),
				[]byte("iteration "+string(rune('0'+i))+"\n"), 0o644)
		}
		if err := SyncRepo(work, "machine-a"); err != nil {
			t.Errorf("sync iteration %d failed: %v", i, err)
		}
	}

	// Repo should be clean
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = work
	out, _ := cmd.Output()
	if len(out) > 0 {
		t.Errorf("repo not clean after rapid sync: %s", out)
	}
}

// setupGitRepoNamed creates a named git repo (avoids temp dir name collisions).
func setupGitRepoNamed(t *testing.T, name string) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, name+"-remote.git")
	work := filepath.Join(tmp, name)

	run := func(dir, cmd string, args ...string) {
		t.Helper()
		c := exec.Command(cmd, args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", cmd, args, err, out)
		}
	}

	run(tmp, "git", "init", "--bare", bare)
	run(tmp, "git", "clone", bare, work)
	run(work, "git", "checkout", "-b", "main")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("# "+name+"\n"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-m", "initial")
	run(work, "git", "push", "-u", "origin", "main")

	return work
}

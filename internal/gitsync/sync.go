// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package gitsync handles git auto-sync (pull, commit, push) for repos.
package gitsync

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncRepo pulls, auto-commits, and pushes a single git repo.
func SyncRepo(repoPath, machineName string) error {
	if !isGitRepo(repoPath) {
		return fmt.Errorf("not a git repo")
	}

	// Check remote is reachable
	if _, err := runCapture(repoPath, "git", "ls-remote", "origin"); err != nil {
		return fmt.Errorf("remote unreachable")
	}

	// Update remote-tracking refs so reconcile can see the latest upstream.
	if _, err := runCapture(repoPath, "git", "fetch", "--quiet"); err != nil {
		return fmt.Errorf("fetch failed")
	}

	// Reconcile Syncthing-delivered content before attempting a pull.
	//
	// When Syncthing delivers a peer's edits as working-tree changes, they can
	// clash with an incoming origin commit that introduces the same content:
	// git aborts the pull with "untracked working tree files would be
	// overwritten" or a merge conflict — even though the file's current
	// content already matches what the pull would write. Detect that case by
	// comparing blob hashes and defer to the remote blob where they match.
	if msg := reconcileSyncthingDelivery(repoPath); msg != "" {
		fmt.Printf("[dotkeeper] %s: %s\n", repoName(repoPath), msg)
	}

	// Pull with rebase
	if _, err := runCapture(repoPath, "git", "pull", "--rebase", "--autostash", "--quiet"); err != nil {
		// Abort rebase and try plain pull. Ignore abort errors — if the rebase
		// wasn't actually in progress this will fail harmlessly, and the pull
		// below is what matters.
		_, _ = runCapture(repoPath, "git", "rebase", "--abort")
		if stderr, err := runCapture(repoPath, "git", "pull", "--autostash", "--quiet"); err != nil {
			return fmt.Errorf("pull failed: %s", strings.TrimSpace(stderr))
		}
	}

	// Stage all changes
	if _, err := runCapture(repoPath, "git", "add", "-A"); err != nil {
		return fmt.Errorf("staging failed")
	}

	// Commit if there are staged changes
	if err := run(repoPath, "git", "diff", "--cached", "--quiet"); err != nil {
		timestamp := time.Now().UTC().Format("2006-01-02 15:04 UTC")
		msg := fmt.Sprintf("auto: %s %s", machineName, timestamp)
		if stderr, err := runCapture(repoPath, "git", "commit", "-m", msg, "--quiet"); err != nil {
			return fmt.Errorf("commit failed: %s", strings.TrimSpace(stderr))
		}
		fmt.Printf("[dotkeeper] %s: committed changes\n", repoName(repoPath))
	}

	// Push if ahead of remote
	ahead := countAhead(repoPath)
	if ahead > 0 {
		if stderr, err := runCapture(repoPath, "git", "push", "--quiet"); err != nil {
			return fmt.Errorf("push failed: %s", strings.TrimSpace(stderr))
		}
		fmt.Printf("[dotkeeper] %s: pushed %d commit(s)\n", repoName(repoPath), ahead)
	} else {
		fmt.Printf("[dotkeeper] %s: up to date\n", repoName(repoPath))
	}

	return nil
}

func isGitRepo(path string) bool {
	return run(path, "git", "rev-parse", "--git-dir") == nil
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// runCapture runs a command and returns stderr on failure for diagnostics.
func runCapture(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func countAhead(dir string) int {
	cmd := exec.Command("git", "rev-list", "--count", "@{upstream}..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	var n int
	// Parse failure returns n=0, which is the correct "unknown" fallback.
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}

func repoName(path string) string {
	return filepath.Base(path)
}

// reconcileSyncthingDelivery resolves the Syncthing+git race where the working
// tree already contains the content of unpulled upstream commits. Returns a
// human-readable status line when it made a change, or empty string otherwise.
// Non-fatal: any errors leave the state untouched for the normal pull path to
// handle.
func reconcileSyncthingDelivery(repoPath string) string {
	upstream, err := runOutput(repoPath, "git", "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return ""
	}
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return ""
	}

	// Fast path: working tree matches upstream exactly. Advance HEAD without
	// touching files. Covers the common case where Syncthing has already
	// delivered every change from an unpulled upstream commit.
	if run(repoPath, "git", "diff", "--quiet", upstream, "--") == nil &&
		run(repoPath, "git", "diff", "--cached", "--quiet", upstream, "--") == nil {
		behind := countBehind(repoPath)
		if behind > 0 {
			if _, err := runCapture(repoPath, "git", "reset", "--hard", upstream); err != nil {
				return ""
			}
			return fmt.Sprintf("reconciled %d upstream commit(s) (working tree already matched)", behind)
		}
		return ""
	}

	// Slow path: partial match. For each untracked or modified file whose
	// current content matches the upstream blob for that path, clear the local
	// copy so the pull can apply cleanly. Files with genuinely local changes
	// are left alone.
	resolved := 0

	untracked, _ := runOutput(repoPath, "git", "ls-files", "--others", "--exclude-standard")
	for _, line := range splitLines(untracked) {
		if matchesUpstream(repoPath, line, upstream) {
			if err := os.Remove(filepath.Join(repoPath, line)); err == nil {
				resolved++
			}
		}
	}

	modified, _ := runOutput(repoPath, "git", "diff", "--name-only")
	for _, line := range splitLines(modified) {
		if matchesUpstream(repoPath, line, upstream) {
			if _, err := runCapture(repoPath, "git", "checkout", upstream, "--", line); err == nil {
				resolved++
			}
		}
	}

	if resolved > 0 {
		return fmt.Sprintf("reconciled %d file(s) already matching upstream", resolved)
	}
	return ""
}

// matchesUpstream reports whether the working-tree file at relPath has the
// same blob hash as the version at <upstream>:<relPath>. Uses git's own
// hash-object so clean filters (e.g. transcrypt) are applied symmetrically.
func matchesUpstream(repoPath, relPath, upstream string) bool {
	if relPath == "" {
		return false
	}
	localHash, err := runOutput(repoPath, "git", "hash-object", "--", relPath)
	if err != nil {
		return false
	}
	localHash = strings.TrimSpace(localHash)

	lsOut, err := runOutput(repoPath, "git", "ls-tree", upstream, "--", relPath)
	if err != nil {
		return false
	}
	fields := strings.Fields(lsOut)
	if len(fields) < 3 || fields[1] != "blob" {
		return false
	}
	return fields[2] == localHash
}

// countBehind returns how many upstream commits are not in HEAD.
func countBehind(dir string) int {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD..@{upstream}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// runOutput runs a command and returns stdout.
func runOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

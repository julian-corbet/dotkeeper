// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package gitsync handles git auto-sync (pull, commit, push) for repos.
package gitsync

import (
	"bytes"
	"fmt"
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

	// Pull with rebase
	if _, err := runCapture(repoPath, "git", "pull", "--rebase", "--autostash", "--quiet"); err != nil {
		// Abort rebase and try plain pull
		runCapture(repoPath, "git", "rebase", "--abort")
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
	fmt.Sscanf(s, "%d", &n)
	return n
}

func repoName(path string) string {
	return filepath.Base(path)
}

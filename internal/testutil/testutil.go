// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package testutil provides shared helpers for dotkeeper tests.
//
// Adding a new test? Use these helpers to avoid boilerplate:
//
//	env := testutil.SetupConfigEnv(t)   // isolated config dir
//	work := testutil.SetupGitRepo(t)    // git repo with bare remote
//	bin := testutil.BuildBinary(t)      // compiled dotkeeper binary
//	testutil.AssertFilePerms(t, path, 0o600)
//	testutil.GoldenUpdate(t, "name", got) // or GoldenCheck
//
// Found a bug? Write a test that reproduces it, then fix the code.
// The test stays forever as a regression guard.
package testutil

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden is set by -update flag: go test -update ./...
var updateGolden = flag.Bool("update", false, "update golden files")

// SetupConfigEnv creates an isolated config/data environment.
// Returns the temp root directory. XDG_CONFIG_HOME and XDG_DATA_HOME
// are set to subdirectories of it.
func SetupConfigEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("HOME", tmp)
	return tmp
}

// SetupGitRepo creates a bare remote and a working clone with one initial
// commit on branch "main". Returns the working directory path.
//
// The environment is configured so git commits don't require user config.
func SetupGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	gitCmd(t, tmp, "init", "--bare", bare)
	gitCmd(t, tmp, "clone", bare, work)
	gitCmd(t, work, "checkout", "-b", "main")

	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# test\n"), 0o644)
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "initial")
	gitCmd(t, work, "push", "-u", "origin", "main")

	return work
}

// SetupGitRepoWithRemote creates a bare remote and two working clones
// for testing multi-machine sync scenarios. Returns (work1, work2, bare).
func SetupGitRepoWithRemote(t *testing.T) (string, string, string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work1 := filepath.Join(tmp, "work1")
	work2 := filepath.Join(tmp, "work2")

	gitCmd(t, tmp, "init", "--bare", bare)
	gitCmd(t, tmp, "clone", bare, work1)
	gitCmd(t, work1, "checkout", "-b", "main")

	_ = os.WriteFile(filepath.Join(work1, "README.md"), []byte("# test\n"), 0o644)
	gitCmd(t, work1, "add", ".")
	gitCmd(t, work1, "commit", "-m", "initial")
	gitCmd(t, work1, "push", "-u", "origin", "main")

	gitCmd(t, tmp, "clone", "-b", "main", bare, work2)

	return work1, work2, bare
}

// gitCmd runs a git command with test-friendly environment.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = GitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// GitEnv returns environment variables for deterministic git operations.
func GitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
}

// BuildBinary compiles the dotkeeper binary and returns its path.
// The binary is cached per test run — safe to call multiple times.
func BuildBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "dotkeeper")

	build := exec.Command("go", "build", "-tags", "noassets", "-o", binary, "./cmd/dotkeeper")
	build.Dir = FindRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

// RunBinary executes the dotkeeper binary with the given args and
// environment root. Returns stdout+stderr and error.
func RunBinary(t *testing.T, binary, envRoot string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = EnvWith(envRoot)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// EnvWith returns os.Environ() plus isolated XDG and HOME paths.
func EnvWith(tmp string) []string {
	return append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(tmp, "config"),
		"XDG_DATA_HOME="+filepath.Join(tmp, "data"),
		"HOME="+tmp,
	)
}

// FindRepoRoot walks up from CWD to find go.mod.
func FindRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

// AssertFilePerms checks that a file has the expected permission bits.
func AssertFilePerms(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	got := info.Mode().Perm()
	if got != want {
		t.Errorf("%s: perm = %o, want %o", filepath.Base(path), got, want)
	}
}

// AssertFileExists checks that a file exists.
func AssertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected file to exist: %s", path)
	}
}

// AssertFileContains checks that a file contains the given substring.
func AssertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("%s does not contain %q", filepath.Base(path), want)
	}
}

// GoldenCheck compares got against a golden file in testdata/.
// Run with -update to regenerate: go test -update ./...
func GoldenCheck(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")

	if *updateGolden {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("updating golden file: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s not found — run with -update to create it", path)
	}
	if string(want) != got {
		t.Errorf("output differs from golden file %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

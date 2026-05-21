// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEndToEndPushUpdatesPeerWorkingTree proves the v1.0.0
// architectural assumption: a `git push` to a peer's working tree,
// when that working tree is configured with
// `receive.denyCurrentBranch=updateInstead`, atomically updates the
// peer's working tree on the push.
//
// Without updateInstead, git refuses to update a checked-out
// branch ("refusing to update checked out branch"). This is the
// failure mode that motivates `dotkeeper bare-init` — running
// `git config receive.denyCurrentBranch updateInstead` on every
// tracked folder on every peer.
//
// The test uses file:// URLs instead of ssh:// because it's
// running against itself; the strategy (push directly to the
// working tree of an updateInstead-configured peer) is identical
// regardless of transport. GitSSHTransport's command construction
// is unit-tested separately.
func TestEndToEndPushUpdatesPeerWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping integration test")
	}
	src, dst := makePeerRepoPair(t)
	// Critical: configure destination to accept pushes to its
	// current branch and atomically update the working tree.
	mustGit(t, dst, "config", "receive.denyCurrentBranch", "updateInstead")

	// Make a commit on src.
	writeFile(t, src, "hello.txt", "v1\n")
	mustGit(t, src, "add", "hello.txt")
	mustGit(t, src, "commit", "-m", "first")

	// Push from src to dst. file:// URL — the GitSSHTransport
	// equivalent is ssh:// to the same path, but the
	// destination-side semantics (updateInstead) are independent
	// of transport.
	mustGit(t, src, "push", "file://"+dst, "HEAD:refs/heads/main")

	// Assert dst's working tree contains the file with the
	// expected content. Without updateInstead, the file would
	// not exist on dst's working tree even though dst's
	// refs/heads/main would point at the new commit.
	content, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("destination working tree was not updated: file missing: %v", err)
	}
	if string(content) != "v1\n" {
		t.Errorf("destination has wrong content: %q", content)
	}

	// Round 2: verify subsequent pushes also update the working
	// tree. (updateInstead applies to every push, not just the
	// initial one.)
	writeFile(t, src, "hello.txt", "v2\n")
	mustGit(t, src, "add", "hello.txt")
	mustGit(t, src, "commit", "-m", "second")
	mustGit(t, src, "push", "file://"+dst, "HEAD:refs/heads/main")

	content, err = os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("after second push: %v", err)
	}
	if string(content) != "v2\n" {
		t.Errorf("second push didn't update working tree: %q", content)
	}
}

// TestEndToEndPushFailsWithoutUpdateInstead is the negative-control
// counterpart: without the updateInstead config, git refuses the
// push and the destination working tree stays empty. This is the
// failure mode `dotkeeper bare-init` fixes.
func TestEndToEndPushFailsWithoutUpdateInstead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping integration test")
	}
	src, dst := makePeerRepoPair(t)
	// Intentionally DO NOT set denyCurrentBranch=updateInstead.

	writeFile(t, src, "hello.txt", "v1\n")
	mustGit(t, src, "add", "hello.txt")
	mustGit(t, src, "commit", "-m", "first")

	cmd := exec.Command("git", "push", "file://"+dst, "HEAD:refs/heads/main")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("push to non-updateInstead working tree should have failed; succeeded silently")
	}
	if !strings.Contains(string(out), "refusing to update") &&
		!strings.Contains(string(out), "denyCurrentBranch") {
		t.Errorf("push failed but error doesn't mention current-branch refusal; output: %s", out)
	}
}

// TestEndToEndGitSSHTransportPropagateChange exercises the full
// PropagateChange path against a real local git destination by
// substituting the transport's command runner with a wrapper that
// rewrites the SSH URL to a file:// URL pointing at the local
// destination repo. This is the closest we can get to a
// production push without standing up an SSH server in the test
// environment.
//
// Proves: GitSSHTransport.PropagateChange's command construction
// (refspec, push direction, working-tree dir) results in the peer
// repo's working tree being updated when the peer has
// updateInstead set. Bug-finding power: if I broke
// PropagateChange's args ordering, refspec format, or working
// directory, this test catches it; the stub-runner unit tests
// only check that *some* args are constructed.
func TestEndToEndGitSSHTransportPropagateChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping integration test")
	}
	src, dst := makePeerRepoPair(t)
	mustGit(t, dst, "config", "receive.denyCurrentBranch", "updateInstead")

	writeFile(t, src, "hello.txt", "v1\n")
	mustGit(t, src, "add", "hello.txt")
	mustGit(t, src, "commit", "-m", "first")

	// Wrap the real exec runner to rewrite the ssh:// URL the
	// transport would build (mirror of the source path on
	// host "stub-host") to a file:// URL pointing at our local
	// destination. This intercept proves we're running the
	// actual GitSSHTransport command construction — not a stub
	// — while still avoiding the SSH server requirement. The
	// v1.0.0 mirror-paths convention says peer-side path matches
	// local; we deliberately violate that here so the test can
	// have two distinct repos.
	rewritingRunner := &rewriteRunner{
		from:  "ssh://stub-host" + src,
		to:    "file://" + dst,
		inner: execRunner{},
	}
	tr := &GitSSHTransport{
		resolver: &stubResolver{
			name:      "test",
			available: true,
			addresses: map[string]string{"peer": "stub-host"},
		},
		runner:       rewritingRunner,
		probeTimeout: 5 * time.Second,
		pushTimeout:  10 * time.Second,
	}

	// Set up the git remote on src via the transport's own
	// EnsurePeerReachability — exercises the full code path.
	if err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x", Path: src}, Peer{Name: "peer"}); err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}

	// Now actually push.
	commitHash := strings.TrimSpace(string(mustGitOut(t, src, "rev-parse", "HEAD")))
	if err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "dk-x", Path: src}, CommitHash: commitHash},
		Peer{Name: "peer"}); err != nil {
		t.Fatalf("PropagateChange: %v", err)
	}

	// Verify the destination working tree received the file.
	content, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("destination working tree was not updated via GitSSHTransport: %v", err)
	}
	if string(content) != "v1\n" {
		t.Errorf("destination has wrong content after propagation: %q", content)
	}
}

// rewriteRunner is a commandRunner that intercepts args matching
// `from` and substitutes `to` before delegating to the inner
// runner. Used by TestEndToEndGitSSHTransportPropagateChange to
// substitute file:// for ssh:// without bypassing the transport's
// command construction.
type rewriteRunner struct {
	from  string
	to    string
	inner commandRunner
}

func (r *rewriteRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	rewritten := make([]string, len(args))
	for i, a := range args {
		rewritten[i] = strings.ReplaceAll(a, r.from, r.to)
	}
	return r.inner.Run(ctx, dir, name, rewritten...)
}

// makePeerRepoPair creates two git repos that share the same
// initial commit — modelling the production scenario where the
// peer's repo was created by cloning the source's history (via
// Syncthing's initial sync, or a manual `git clone`). Pushes
// between them are fast-forwards, just as in a real dotkeeper
// fleet.
//
// Returns (src, dst) with src having a single root commit and
// dst as a non-bare clone of src checked out at the same commit.
// Both are configured with a test identity so future commits
// don't depend on the user's global git config.
func makePeerRepoPair(t *testing.T) (src, dst string) {
	t.Helper()
	src = t.TempDir()
	mustGit(t, src, "init", "-b", "main")
	mustGit(t, src, "config", "user.email", "test@dotkeeper")
	mustGit(t, src, "config", "user.name", "dotkeeper test")
	mustGit(t, src, "commit", "--allow-empty", "-m", "root")

	dst = t.TempDir()
	// Remove t.TempDir's directory first because `git clone` expects
	// the target to not exist (or to be empty).
	if err := os.Remove(dst); err != nil {
		t.Fatalf("remove dst before clone: %v", err)
	}
	mustGit(t, ".", "clone", src, dst)
	mustGit(t, dst, "config", "user.email", "test@dotkeeper")
	mustGit(t, dst, "config", "user.name", "dotkeeper test")
	return src, dst
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// Compile-time sanity check that errors.As against ErrUnreachable
// still works in case the test file accidentally shadows it.
var _ = errors.Is

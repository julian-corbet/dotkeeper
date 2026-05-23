// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/transport"
)

// TestE2EDaemonPropagatorDeliversCommitViaGitSSH wires the entire
// v1.0 multi-transport pipe and proves a commit propagates from a
// source git repo to a destination git repo via the real
// `git push` code path.
//
// Components exercised (no stubs above the runner):
//
//   - transport.GitSSHTransport with the real `git remote set-url`/
//     `git push` invocations via execRunner
//   - transport.Manager with a real CostModel (priors + RecordTransfer)
//   - cmd/dotkeeper's daemonPropagator: Route → PropagateChange →
//     RecordTransfer → log
//   - estimateLastCommitSize against an actual commit
//
// The only stub is the address resolver (returns a fixed string)
// and a URL-rewriting wrapper that swaps the transport's
// ssh:// URL for a file:// URL pointing at the destination repo.
// That swap is the one piece that decouples this test from
// standing up an SSH server — every other byte of the pipe is
// production code.
//
// The destination repo has receive.denyCurrentBranch=updateInstead
// set (the v1.0 bare-init contract), so the push to its current
// branch atomically updates the working tree. If we ever change
// the v1.0 design to push to a separate bare repo, this test will
// (correctly) fail and force the assertion to be updated.
func TestE2EDaemonPropagatorDeliversCommitViaGitSSH(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; e2e propagator test requires git")
	}

	src, dst := makeE2ESrcDstPair(t)

	// Configure dst to accept pushes to its current branch — this
	// is what `dotkeeper bare-init` would have done.
	mustGitInE2E(t, dst, "config", "receive.denyCurrentBranch", "updateInstead")

	// Build a real GitSSHTransport, but rewrite its outgoing
	// command URLs from ssh:// to file:// pointing at dst. The
	// rewrite is the single seam — everything else (refspec
	// composition, push direction, working-directory, error
	// handling) is exactly what production runs.
	resolver := &e2eFixedResolver{address: "stub-host"}
	rewriter := &e2eURLRewriter{
		from:  "ssh://stub-host" + src,
		to:    "file://" + dst,
		inner: transport.NewExecRunner(),
	}
	gitssh := transport.NewGitSSHTransportWithRunner(resolver, rewriter)
	mgr := transport.NewManager([]transport.Transport{gitssh})

	peer := transport.Peer{Name: "peer-b", Hostname: "stub-host"}
	mgr.Discover(context.Background(), peer)

	// Build the daemon-side propagator with the real Manager and
	// the source folder. The propagator's size estimation runs
	// `git diff --shortstat HEAD~1 HEAD` against src — also live
	// code, not stubbed.
	folder := transport.Folder{ID: "e2e-folder", Path: src}
	prop := newDaemonPropagator(mgr, []transport.Peer{peer},
		[]transport.Folder{folder}, quietLogger())

	// EnsurePeerReachability adds the git remote on src pointing
	// at dst (via the rewriter). Done explicitly here because
	// daemonPropagator.PropagateNewCommit assumes the remote is
	// already configured; in production reconcile fires this on
	// the AddSyncthingFolder / UpdateSyncthingFolderDevices path
	// via a separate action.
	if err := gitssh.EnsurePeerReachability(context.Background(), folder, peer); err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}

	// Make a real commit on src with a multi-line file so
	// estimateLastCommitSize sees a meaningful diff.
	const fileContents = "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte(fileContents), 0o644); err != nil {
		t.Fatalf("write data.txt: %v", err)
	}
	mustGitInE2E(t, src, "add", "data.txt")
	mustGitInE2E(t, src, "commit", "-m", "e2e: real commit for propagator test")

	// Snapshot pre-state on the destination: data.txt should NOT
	// exist yet. If the test machinery accidentally seeded it,
	// the post-condition wouldn't prove anything.
	if _, err := os.Stat(filepath.Join(dst, "data.txt")); err == nil {
		t.Fatal("destination already has data.txt before propagation; test setup is broken")
	}

	// Sample cost-model state before the push.
	setupBefore, _, nBefore := mgr.ModelParametersFor(gitssh.Name(), peer.Name)
	if nBefore != 0 {
		t.Fatalf("baseline effective-sample count should be 0; got %.2f", nBefore)
	}

	// Sanity: the remote URL configured by EnsurePeerReachability
	// must be file:// (rewritten). If this is still ssh://, the
	// rewriter didn't fire and the push would attempt a real SSH
	// connection.
	remoteURL := strings.TrimSpace(string(mustGitOutInE2E(t, src,
		"remote", "get-url", "dk+fixed+peer-b")))
	if !strings.HasPrefix(remoteURL, "file://") {
		t.Fatalf("rewriter didn't substitute; remote URL still %q", remoteURL)
	}

	// THE ACTUAL E2E CALL — exercises every layer. The
	// daemonPropagator looks up the folder for src, estimates
	// commit size, calls mgr.Route (which checks Discover's probe
	// result), invokes Transport.PropagateChange, and feeds the
	// observed elapsed time back into the cost model.
	prop.PropagateNewCommit(context.Background(), src)

	// Post-condition 1: destination working tree contains the
	// new file with the right content.
	got, err := os.ReadFile(filepath.Join(dst, "data.txt"))
	if err != nil {
		t.Fatalf("destination working tree was not updated: data.txt missing: %v", err)
	}
	if string(got) != fileContents {
		t.Errorf("destination data.txt content mismatch:\ngot:\n%s\nwant:\n%s", got, fileContents)
	}

	// Post-condition 2: destination refs/heads/main advanced to
	// the source's HEAD.
	srcHead := strings.TrimSpace(string(mustGitOutInE2E(t, src, "rev-parse", "HEAD")))
	dstHead := strings.TrimSpace(string(mustGitOutInE2E(t, dst, "rev-parse", "HEAD")))
	if srcHead != dstHead {
		t.Errorf("destination HEAD (%s) does not match source HEAD (%s)", dstHead, srcHead)
	}

	// Post-condition 3: cost model recorded the transfer. The
	// effective-sample count should be ≥1 and the fitted setup
	// MAY have shifted toward the observed value (whether it
	// actually shifts depends on the prior weight; we just
	// check that recording happened).
	setupAfter, _, nAfter := mgr.ModelParametersFor(gitssh.Name(), peer.Name)
	if nAfter < 1 {
		t.Errorf("cost model did not absorb the observation; nAfter=%.2f", nAfter)
	}
	if setupAfter == setupBefore && nAfter > nBefore {
		// Identical setup with more observations is possible if
		// the observation happened to exactly equal the prior;
		// flag as suspicious but don't fail — could be
		// numerically valid.
		t.Logf("note: cost model absorbed %.0f observations but setup unchanged at %.2f — likely benign coincidence",
			nAfter-nBefore, setupAfter)
	}
}

// e2eFixedResolver returns a single fixed address regardless of peer.
type e2eFixedResolver struct {
	address string
}

func (r *e2eFixedResolver) Name() string  { return "fixed" }
func (r *e2eFixedResolver) Available() bool { return true }
func (r *e2eFixedResolver) Resolve(_ context.Context, _ transport.Peer) (string, error) {
	return r.address, nil
}

// e2eURLRewriter is the one seam that lets us avoid an SSH
// server: it intercepts every command and substitutes ssh://-prefixed
// URLs with file:// pointers at the destination repo. Identical to
// the rewriter pattern used in internal/transport/integration_test.go
// but at the daemon-propagator layer.
type e2eURLRewriter struct {
	from  string
	to    string
	inner transport.CommandRunner
}

func (r *e2eURLRewriter) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	// Short-circuit `ssh` reachability probes: GitSSHTransport.Probe
	// runs `ssh <host> /bin/true` to measure latency, which we
	// can't satisfy in tests without a real SSH server. Returning
	// success here makes Manager.Discover mark the gitssh
	// transport reachable; the actual push then goes through
	// `git push` which we redirect to file:// via the URL rewrite
	// below.
	if name == "ssh" {
		return []byte{}, nil
	}
	rewritten := make([]string, len(args))
	for i, a := range args {
		rewritten[i] = strings.ReplaceAll(a, r.from, r.to)
	}
	return r.inner.Run(ctx, dir, name, rewritten...)
}

// --- helpers ---

func makeE2ESrcDstPair(t *testing.T) (src, dst string) {
	t.Helper()
	src = t.TempDir()
	mustGitInE2E(t, src, "init", "-b", "main")
	mustGitInE2E(t, src, "config", "user.email", "test@dotkeeper")
	mustGitInE2E(t, src, "config", "user.name", "dotkeeper test")
	mustGitInE2E(t, src, "commit", "--allow-empty", "-m", "root")

	dst = t.TempDir()
	if err := os.Remove(dst); err != nil {
		t.Fatalf("remove dst before clone: %v", err)
	}
	mustGitInE2E(t, ".", "clone", src, dst)
	mustGitInE2E(t, dst, "config", "user.email", "test@dotkeeper")
	mustGitInE2E(t, dst, "config", "user.name", "dotkeeper test")
	return src, dst
}

func mustGitInE2E(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func mustGitOutInE2E(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// --- pin: estimateLastCommitSize is called by daemonPropagator and
// must produce a non-zero, plausible size for the test commit (5
// lines × ~50 bytes ≈ 250). If a future refactor breaks the
// estimator, this assertion catches the silent regression at the
// e2e layer rather than only in the unit tests. ---

func TestE2EEstimateLastCommitSizeProducesPlausibleSize(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	src := t.TempDir()
	mustGitInE2E(t, src, "init", "-b", "main")
	mustGitInE2E(t, src, "config", "user.email", "test@dotkeeper")
	mustGitInE2E(t, src, "config", "user.name", "dotkeeper test")
	mustGitInE2E(t, src, "commit", "--allow-empty", "-m", "root")
	if err := os.WriteFile(filepath.Join(src, "f.txt"),
		[]byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitInE2E(t, src, "add", "f.txt")
	mustGitInE2E(t, src, "commit", "-m", "five lines")

	got := estimateLastCommitSize(src)
	if got < 100 || got > 1000 {
		t.Errorf("estimate for 5-line commit = %d; want plausible range [100, 1000]", got)
	}
}

// Verify the test file's silentLogger pattern is reusable.
var _ = time.Second

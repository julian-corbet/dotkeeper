// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/transport"
)

// Tests for the v1.0 daemon-side propagator wiring in main.go.
// The internal/transport package has its own thorough unit tests;
// these exercise the cmd/dotkeeper integration layer that picks a
// transport via Manager.Route, executes the push, and feeds
// observed elapsed back via RecordTransfer.

// recordingTransport implements transport.Transport for the
// propagator tests. Captures PropagateChange invocations and lets
// the test inject a per-call error.
type recordingTransport struct {
	name string
	mu   sync.Mutex
	// recordedSizes lists the SizeHint values from PropagateChange
	// calls in invocation order. The propagator passes the size
	// estimate through to the transport unchanged, so this is the
	// test's window into "what did the propagator decide to push?"
	recordedSizes []int64
	// recordedPeers lists the peer names PropagateChange was
	// called against. One entry per call, parallel to recordedSizes.
	recordedPeers []string
	// pushErr, if non-nil, is returned from PropagateChange on
	// every call. Used to exercise the propagator's error-handling
	// branch.
	pushErr error
	// async, when true, makes PropagatesSynchronously report false —
	// used to exercise the propagator's "skip RecordTransfer for
	// async transports" path. Defaults to synchronous (true via the
	// zero-value inversion in the method below).
	async bool
}

func (t *recordingTransport) Name() string    { return t.name }
func (t *recordingTransport) Available() bool { return true }
func (t *recordingTransport) EnsurePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (t *recordingTransport) RemovePeerReachability(_ context.Context, _ transport.Folder, _ transport.Peer) error {
	return nil
}
func (t *recordingTransport) Probe(_ context.Context, _ transport.Peer) (time.Duration, error) {
	return time.Millisecond, nil
}
func (t *recordingTransport) PropagateChange(_ context.Context, c transport.Change, p transport.Peer) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordedSizes = append(t.recordedSizes, c.SizeHint)
	t.recordedPeers = append(t.recordedPeers, p.Name)
	return t.pushErr
}
func (t *recordingTransport) PropagatesSynchronously() bool { return !t.async }

// quietLogger discards everything. The propagator logs success
// and failure paths; we don't care about the output, just the
// behaviour. Distinct name from the package-local silentLogger
// in daemon_test.go to avoid the duplicate symbol.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopPropWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

type noopPropWriter struct{}

func (noopPropWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestDaemonPropagatorFansOutToAllPeers(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	peerA := transport.Peer{Name: "a"}
	peerB := transport.Peer{Name: "b"}
	mgr.Discover(context.Background(), peerA)
	mgr.Discover(context.Background(), peerB)

	folder := transport.Folder{ID: "dk-test", Path: "/some/path"}
	prop := newDaemonPropagator(mgr, []transport.Peer{peerA, peerB},
		[]transport.Folder{folder}, quietLogger())

	prop.PropagateNewCommit(context.Background(), folder.Path)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 2 {
		t.Fatalf("expected 2 PropagateChange calls (one per peer), got %d: %v",
			len(tr.recordedPeers), tr.recordedPeers)
	}
	got := map[string]bool{tr.recordedPeers[0]: true, tr.recordedPeers[1]: true}
	if !got["a"] || !got["b"] {
		t.Errorf("expected fanout to both peers a,b; got %v", tr.recordedPeers)
	}
}

func TestDaemonPropagatorIgnoresUnknownFolder(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	peer := transport.Peer{Name: "a"}
	mgr.Discover(context.Background(), peer)

	// Propagator is built with NO folders. PropagateNewCommit
	// receives a path it doesn't recognise — should log and
	// no-op, not crash.
	prop := newDaemonPropagator(mgr, []transport.Peer{peer},
		nil, quietLogger())
	prop.PropagateNewCommit(context.Background(), "/never/heard/of")

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 0 {
		t.Errorf("PropagateChange called for unknown folder; calls=%v", tr.recordedPeers)
	}
}

func TestDaemonPropagatorNoPeersIsNoop(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	folder := transport.Folder{ID: "x", Path: "/p"}
	prop := newDaemonPropagator(mgr, nil, []transport.Folder{folder}, quietLogger())
	prop.PropagateNewCommit(context.Background(), folder.Path)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 0 {
		t.Errorf("PropagateChange called with empty peer roster; calls=%v", tr.recordedPeers)
	}
}

func TestDaemonPropagatorRecordsObservationOnSuccess(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	peer := transport.Peer{Name: "a"}
	mgr.Discover(context.Background(), peer)

	// Sample model parameters before propagation. After a successful
	// push the model should have absorbed a real observation (n
	// goes from 0 to ≥1).
	_, _, nBefore := mgr.ModelParametersFor("stub", "a")
	if nBefore != 0 {
		t.Fatalf("baseline effective-sample count should be 0, got %.2f", nBefore)
	}

	folder := transport.Folder{ID: "x", Path: "/p"}
	prop := newDaemonPropagator(mgr, []transport.Peer{peer},
		[]transport.Folder{folder}, quietLogger())
	prop.PropagateNewCommit(context.Background(), folder.Path)

	_, _, nAfter := mgr.ModelParametersFor("stub", "a")
	if nAfter < 1 {
		t.Errorf("propagator did not Record the transfer; effective-sample count = %.2f, want >= 1", nAfter)
	}
}

func TestDaemonPropagatorSkipsRecordForAsyncTransport(t *testing.T) {
	// Asynchronous transports (SyncthingTransport in production)
	// return from PropagateChange in microseconds because the actual
	// work runs in a background system. Feeding that ~µs elapsed
	// into the cost model would teach Manager.Route that the
	// transport is infinitely fast — every subsequent routing
	// decision would then pick the async transport, even when a
	// synchronous transport is the better choice for a small
	// payload. This test pins the propagator's "skip RecordTransfer
	// when PropagatesSynchronously()=false" guard.
	tr := &recordingTransport{name: "async-stub", async: true}
	mgr := transport.NewManager([]transport.Transport{tr})
	peer := transport.Peer{Name: "a"}
	mgr.Discover(context.Background(), peer)

	folder := transport.Folder{ID: "x", Path: "/p"}
	prop := newDaemonPropagator(mgr, []transport.Peer{peer},
		[]transport.Folder{folder}, quietLogger())
	prop.PropagateNewCommit(context.Background(), folder.Path)

	// Push must still have happened — the cost-model skip is
	// independent of whether the transport's hand-off succeeded.
	tr.mu.Lock()
	calls := len(tr.recordedPeers)
	tr.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 PropagateChange call, got %d", calls)
	}

	_, _, nAfter := mgr.ModelParametersFor("async-stub", "a")
	if nAfter != 0 {
		t.Errorf("async transport poisoned cost model; effective-sample count = %.2f, want 0", nAfter)
	}
}

func TestDaemonPropagatorSkipsRecordOnPushFailure(t *testing.T) {
	// Failed pushes are noise, not signal — the cost model would be
	// misled by recording a fast-failing push as a real transfer.
	tr := &recordingTransport{name: "stub", pushErr: errors.New("simulated push failure")}
	mgr := transport.NewManager([]transport.Transport{tr})
	peer := transport.Peer{Name: "a"}
	mgr.Discover(context.Background(), peer)

	folder := transport.Folder{ID: "x", Path: "/p"}
	prop := newDaemonPropagator(mgr, []transport.Peer{peer},
		[]transport.Folder{folder}, quietLogger())
	prop.PropagateNewCommit(context.Background(), folder.Path)

	_, _, nAfter := mgr.ModelParametersFor("stub", "a")
	if nAfter != 0 {
		t.Errorf("failed push contributed to cost model; effective-sample count = %.2f, want 0", nAfter)
	}
}

func TestDaemonPropagatorContinuesPastPeerWithoutRoute(t *testing.T) {
	// Manager.Route returns ErrNoRoute for peers it has never
	// Discover'd. The propagator must log and continue to the
	// next peer rather than abort the whole fan-out.
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	known := transport.Peer{Name: "known"}
	unknown := transport.Peer{Name: "never-discovered"}
	mgr.Discover(context.Background(), known)
	// Deliberately skip mgr.Discover(unknown).

	folder := transport.Folder{ID: "x", Path: "/p"}
	prop := newDaemonPropagator(mgr, []transport.Peer{unknown, known},
		[]transport.Folder{folder}, quietLogger())
	prop.PropagateNewCommit(context.Background(), folder.Path)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 1 || tr.recordedPeers[0] != "known" {
		t.Errorf("expected propagator to skip the unknown peer and push to 'known'; recordedPeers=%v",
			tr.recordedPeers)
	}
}

// TestDaemonPropagatorPicksUpPeerAddedAfterConstruction pins the
// freshness contract introduced in v1.0.2: peers added to the
// machine roster (machine.toml) after the daemon started must
// receive pushes on the next commit, without waiting for a
// restart. Before the fix the propagator captured the peer slice
// at construction and silently skipped late-added peers.
func TestDaemonPropagatorPicksUpPeerAddedAfterConstruction(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	folder := transport.Folder{ID: "x", Path: "/p"}

	// peers is mutated mid-test to simulate a roster change.
	var peers []transport.Peer
	prop := newDaemonPropagatorWithSources(mgr,
		func() []transport.Peer { return peers },
		func() map[string]transport.Folder {
			return map[string]transport.Folder{folder.Path: folder}
		},
		quietLogger())

	// Initial commit with empty roster — must be a no-op.
	prop.PropagateNewCommit(context.Background(), folder.Path)
	tr.mu.Lock()
	initial := len(tr.recordedPeers)
	tr.mu.Unlock()
	if initial != 0 {
		t.Fatalf("expected no fanout for empty roster; got %d calls", initial)
	}

	// Add a peer post-construction. Discover it so Manager.Route
	// has a reachable transport entry.
	later := transport.Peer{Name: "late-joiner"}
	mgr.Discover(context.Background(), later)
	peers = []transport.Peer{later}

	// Next commit must push to the new peer.
	prop.PropagateNewCommit(context.Background(), folder.Path)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 1 || tr.recordedPeers[0] != "late-joiner" {
		t.Errorf("expected push to late-joiner; recordedPeers=%v", tr.recordedPeers)
	}
}

// TestDaemonPropagatorPicksUpFolderAddedAfterConstruction is the
// folder-side analogue of the peer-freshness pin. A folder added
// to the managed set after daemon start must propagate on its
// first commit cycle.
func TestDaemonPropagatorPicksUpFolderAddedAfterConstruction(t *testing.T) {
	tr := &recordingTransport{name: "stub"}
	mgr := transport.NewManager([]transport.Transport{tr})
	peer := transport.Peer{Name: "a"}
	mgr.Discover(context.Background(), peer)

	folders := make(map[string]transport.Folder)
	prop := newDaemonPropagatorWithSources(mgr,
		func() []transport.Peer { return []transport.Peer{peer} },
		func() map[string]transport.Folder { return folders },
		quietLogger())

	// Folder unknown at first call.
	prop.PropagateNewCommit(context.Background(), "/p")
	tr.mu.Lock()
	initial := len(tr.recordedPeers)
	tr.mu.Unlock()
	if initial != 0 {
		t.Fatalf("expected no push for unknown folder; got %d calls", initial)
	}

	// Add the folder post-construction.
	folders["/p"] = transport.Folder{ID: "x", Path: "/p"}

	prop.PropagateNewCommit(context.Background(), "/p")
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.recordedPeers) != 1 {
		t.Errorf("expected push after folder added; recordedPeers=%v", tr.recordedPeers)
	}
}

// --- estimateLastCommitSize tests ---

func setupTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping repo-fixture test")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@dotkeeper"},
		{"config", "user.name", "dotkeeper test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestEstimateLastCommitSizeOnFirstCommit(t *testing.T) {
	// On the first commit there is no HEAD~1; `git diff HEAD~1 HEAD`
	// fails and we documented that estimateLastCommitSize returns 0
	// in that case.
	dir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "f.txt")
	runGitInDir(t, dir, "commit", "-m", "first")

	got := estimateLastCommitSize(dir)
	if got != 0 {
		t.Errorf("first-commit estimate = %d, want 0 (HEAD~1 doesn't exist)", got)
	}
}

func TestEstimateLastCommitSizeOnSubsequentCommit(t *testing.T) {
	// With two commits, HEAD~1 exists. The shortstat parser should
	// extract the inserted-line count and multiply by 50 (the
	// average-line-bytes proxy).
	dir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "a.txt")
	runGitInDir(t, dir, "commit", "-m", "first")

	body := strings.Repeat("line\n", 10)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "b.txt")
	runGitInDir(t, dir, "commit", "-m", "second")

	got := estimateLastCommitSize(dir)
	// Expect 10 insertions × 50 bytes/line proxy = 500. The exact
	// value depends on git's shortstat format; we accept anything
	// in the "reasonable for ten lines" range to allow for the
	// pluralisation/format variations the parser is loose about.
	if got < 100 || got > 1000 {
		t.Errorf("ten-line commit estimate = %d, want a 'reasonable for 10 lines' value (100..1000)", got)
	}
}

func TestEstimateLastCommitSizeOnPureDeletion(t *testing.T) {
	dir := setupTestRepo(t)
	body := strings.Repeat("line\n", 5)
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "c.txt")
	runGitInDir(t, dir, "commit", "-m", "add")

	runGitInDir(t, dir, "rm", "c.txt")
	runGitInDir(t, dir, "commit", "-m", "delete")

	// Pure-deletion commit: shortstat has "Y deletions(-)" but no
	// "insertions". The parser must handle the no-insertions case
	// without crashing or returning a negative size.
	got := estimateLastCommitSize(dir)
	if got <= 0 {
		t.Errorf("pure-deletion estimate = %d, want > 0 (5 deletions ≈ 250 bytes)", got)
	}
}

func TestEstimateLastCommitSizeOnBrokenRepo(t *testing.T) {
	// Pointing at a non-git directory: `git diff` fails; the function
	// is documented to return 0 in that case rather than propagating
	// the error.
	dir := t.TempDir()
	got := estimateLastCommitSize(dir)
	if got != 0 {
		t.Errorf("non-git dir estimate = %d, want 0", got)
	}
}

func TestNumberBeforeParsesShortstatVariants(t *testing.T) {
	// shortstat format covers singular/plural pluralisation, both
	// insertion-only and deletion-only commits, mixed commits, and
	// commits with file-renames-only (no insertions or deletions).
	// The parser must handle all of them without crashing.
	cases := []struct {
		name   string
		input  string
		marker string
		want   int64
	}{
		{"plural insertions", " 1 file changed, 10 insertions(+)", "insertion", 10},
		{"singular insertion", " 1 file changed, 1 insertion(+)", "insertion", 1},
		{"plural deletions only", " 1 file changed, 5 deletions(-)", "deletion", 5},
		{"mixed", " 2 files changed, 3 insertions(+), 4 deletions(-)", "insertion", 3},
		{"mixed deletions", " 2 files changed, 3 insertions(+), 4 deletions(-)", "deletion", 4},
		{"marker absent", " 1 file changed", "insertion", 0},
		{"large number", " 1 file changed, 1234567 insertions(+)", "insertion", 1234567},
		{"empty input", "", "insertion", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := numberBefore(c.input, c.marker)
			if got != c.want {
				t.Errorf("numberBefore(%q, %q) = %d, want %d", c.input, c.marker, got, c.want)
			}
		})
	}
}

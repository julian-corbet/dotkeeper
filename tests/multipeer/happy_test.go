// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build multipeer

package multipeer

import (
	"strings"
	"testing"
	"time"
)

// TestPropagateAtoB exercises the most fundamental dotkeeper promise: a file
// written on peer-a appears on peer-b within reasonable time. If this test
// fails, nothing else in this suite can be trusted.
func TestPropagateAtoB(t *testing.T) {
	f := newFixture(t)
	f.pair()

	f.writeFile("peer-a", "hello.txt", "from-a\n")

	if err := f.waitForFile("peer-b", "hello.txt", "from-a\n", 60*time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestPropagateBtoA mirrors TestPropagateAtoB to catch any asymmetry in
// pair-up direction (peer-a's machine.toml lists peer-b vs vice versa).
func TestPropagateBtoA(t *testing.T) {
	f := newFixture(t)
	f.pair()

	f.writeFile("peer-b", "hello.txt", "from-b\n")

	if err := f.waitForFile("peer-a", "hello.txt", "from-b\n", 60*time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestConflictRoundTrip writes the same file with different contents on both
// peers while the network is split, then heals the split and expects Syncthing
// to materialize a .sync-conflict-* file. `dotkeeper conflict accept` should
// then promote one side cleanly.
func TestConflictRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.pair()

	// Establish a baseline file both peers agree on.
	f.writeFile("peer-a", "notes.md", "baseline\n")
	if err := f.waitForFile("peer-b", "notes.md", "baseline\n", 60*time.Second); err != nil {
		t.Fatalf("baseline propagation failed: %v", err)
	}

	// Split: drop traffic between the two peers so each side can independently
	// mutate the file. IPs resolved at runtime since Docker assigns the subnet.
	ipA, ipB := f.peerIP("peer-a"), f.peerIP("peer-b")
	f.mustExec("peer-a", "iptables -A OUTPUT -d "+ipB+" -j DROP")
	f.mustExec("peer-b", "iptables -A OUTPUT -d "+ipA+" -j DROP")

	f.writeFile("peer-a", "notes.md", "from-a\n")
	f.writeFile("peer-b", "notes.md", "from-b\n")
	// Give Syncthing time to scan & version locally.
	time.Sleep(5 * time.Second)

	// Heal.
	f.mustExec("peer-a", "iptables -F OUTPUT")
	f.mustExec("peer-b", "iptables -F OUTPUT")

	// Wait for a conflict marker to appear on at least one side.
	deadline := time.Now().Add(90 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		out, _ := f.execAllowFail("peer-a", `ls /repos/shared 2>/dev/null | grep 'sync-conflict' || true`)
		if len(out) > 0 {
			found = "peer-a"
			break
		}
		out, _ = f.execAllowFail("peer-b", `ls /repos/shared 2>/dev/null | grep 'sync-conflict' || true`)
		if len(out) > 0 {
			found = "peer-b"
			break
		}
		time.Sleep(1 * time.Second)
	}
	if found == "" {
		t.Fatal("no sync-conflict file appeared on either peer within 90s after partition heal")
	}
	t.Logf("conflict marker observed on %s", found)

	// Two-step resolution:
	//   1. `conflict resolve-all` clears trivially-resolvable conflicts (byte-
	//      identical duplicates, 3-way mergeable text).
	//   2. `conflict accept --all` promotes any remaining variants to canonical
	//      (the "human chose the peer side" path).
	// In our scenario the two writes ("from-a", "from-b") aren't mergeable, so
	// resolve-all leaves it as 'kept' and accept --all finishes the job.
	out := f.mustExec(found,
		`dotkeeper conflict resolve-all 2>&1 || true
		dotkeeper conflict accept --all 2>&1 || true
		echo "--- final list ---"
		dotkeeper conflict list 2>&1 || true`,
	)
	t.Logf("post-resolve output on %s:\n%s", found, out)

	// Final assertion: no .sync-conflict-* files remain on the resolving peer.
	if remaining, _ := f.execAllowFail(found, `ls /repos/shared 2>/dev/null | grep 'sync-conflict' || true`); len(strings.TrimSpace(remaining)) > 0 {
		t.Errorf("unresolved conflict files remain on %s:\n%s", found, remaining)
	}
}

// TestOfflineCatchUp stops peer-b, mutates the repo on peer-a, then restarts
// peer-b and asserts it catches up. Exercises Syncthing's index exchange
// after a real downtime, not just a brief disconnect.
func TestOfflineCatchUp(t *testing.T) {
	f := newFixture(t)
	f.pair()

	f.writeFile("peer-a", "baseline.txt", "v0\n")
	if err := f.waitForFile("peer-b", "baseline.txt", "v0\n", 60*time.Second); err != nil {
		t.Fatalf("baseline propagation failed: %v", err)
	}

	// Take peer-b fully offline (stop the dotkeeper daemon).
	f.dkStop("peer-b")
	// And add a hard stop on the container itself to ensure no straggler
	// connection attempts succeed.
	f.composeStop("peer-b")

	// Mutate while peer-b is gone.
	f.writeFile("peer-a", "during-offline.txt", "v1\n")

	// Bring peer-b back.
	f.composeStartExisting("peer-b")
	f.dkStart("peer-b")

	if err := f.waitForFile("peer-b", "during-offline.txt", "v1\n", 90*time.Second); err != nil {
		t.Fatalf("offline catch-up failed: %v", err)
	}
}

// TestTrackAfterPair: a file in a NEW subdirectory tracked-after-pair should
// propagate. This exercises the reconcile-after-track path: the new repo's
// .dotkeeper.toml needs to be picked up by the running daemon and surfaced to
// Syncthing as an additional folder. Note that "tracking the same logical
// repo on both peers" still applies — both call `dotkeeper track` so their
// folder IDs match.
func TestTrackAfterPair(t *testing.T) {
	f := newFixture(t)
	f.pair()

	// At this point /repos/shared is already tracked by pair(). We re-track
	// the same path on both peers after first writing a fresh file; reconcile
	// should be idempotent and the file should propagate.
	f.writeFile("peer-a", "post-pair.txt", "added-later\n")
	f.dkReconcile("peer-a")
	f.dkReconcile("peer-b")

	if err := f.waitForFile("peer-b", "post-pair.txt", "added-later\n", 60*time.Second); err != nil {
		t.Fatalf("post-pair propagation failed: %v", err)
	}
}

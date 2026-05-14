// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build multipeer

// Adversarial scenarios. These primarily *map weaknesses* rather than assert
// strict behavior — when one of these fails it's information about what
// dotkeeper does (or doesn't do) under stress, not necessarily a regression.
//
// Each test logs the observed behavior so a CI run becomes a reproducible
// adversarial report. Tests that observe a *crash, hang, or data-loss* are
// hard failures; tests that observe surprising-but-not-dangerous behavior
// are soft fails via t.Logf so the rest of the suite still runs.

package multipeer

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestClockSkew runs peer-b with a clock offset 1h in the past via libfaketime,
// then triggers a conflict-write race. Syncthing's modtime-based "newest wins"
// tie-breaking should favor peer-a's writes; but the test documents what
// actually happens rather than enforcing a specific winner — the goal is
// to surface clock-skew sensitivity in dotkeeper's conflict heuristic.
func TestClockSkew(t *testing.T) {
	f := newFixture(t)
	// Apply faketime to peer-b BEFORE dotkeeper starts so Syncthing inherits it.
	// libfaketime is preloaded via FAKETIME env on the dotkeeper invocations.
	f.mustExec("peer-b", `echo 'export FAKETIME="-1h"' > /etc/profile.d/faketime.sh`)
	f.mustExec("peer-b", `echo 'export LD_PRELOAD=/usr/lib/faketime/libfaketime.so.1' >> /etc/profile.d/faketime.sh`)

	f.pair()

	// Baseline.
	f.writeFile("peer-a", "skewed.txt", "from-a\n")
	if err := f.waitForFile("peer-b", "skewed.txt", "from-a\n", 60*time.Second); err != nil {
		t.Fatalf("baseline failed: %v", err)
	}

	// Race: both write at "the same time" by wall clock, but peer-b's wall
	// clock thinks it's an hour earlier. Modtime ordering should put peer-a
	// later → peer-a wins.
	ipA, ipB := f.peerIP("peer-a"), f.peerIP("peer-b")
	f.mustExec("peer-a", "iptables -A OUTPUT -d "+ipB+" -j DROP")
	f.mustExec("peer-b", "iptables -A OUTPUT -d "+ipA+" -j DROP")
	f.writeFile("peer-a", "skewed.txt", "from-a-rewrite\n")
	f.writeFile("peer-b", "skewed.txt", "from-b-rewrite\n")
	time.Sleep(3 * time.Second)
	f.mustExec("peer-a", "iptables -F OUTPUT")
	f.mustExec("peer-b", "iptables -F OUTPUT")

	// Wait for convergence (or conflict file).
	time.Sleep(15 * time.Second)
	a, _ := f.execAllowFail("peer-a", `cat /repos/shared/skewed.txt 2>/dev/null; echo "---"; ls /repos/shared | grep sync-conflict || true`)
	b, _ := f.execAllowFail("peer-b", `cat /repos/shared/skewed.txt 2>/dev/null; echo "---"; ls /repos/shared | grep sync-conflict || true`)
	t.Logf("CLOCK-SKEW OUTCOME — peer-a state:\n%s\npeer-b state:\n%s", a, b)
	// We don't hard-assert a winner; we observe and report.
}

// TestNetworkPartitionMidSync starts a sync of a moderately large file, drops
// the bridge mid-stream, and asserts the transfer resumes/completes after
// the partition is healed. Catches any code path that gives up on a partial
// transfer instead of letting Syncthing's block exchange resume.
func TestNetworkPartitionMidSync(t *testing.T) {
	f := newFixture(t)
	f.pair()

	// 8 MB file — large enough that the transfer is interruptible on the
	// CI runner but small enough to finish within the timeout.
	f.mustExec("peer-a", `dd if=/dev/urandom of=/repos/shared/large.bin bs=1M count=8 2>/dev/null`)

	// Partition almost immediately, then heal 3s later.
	time.Sleep(500 * time.Millisecond)
	ipA, ipB := f.peerIP("peer-a"), f.peerIP("peer-b")
	f.mustExec("peer-a", "iptables -A OUTPUT -d "+ipB+" -j DROP")
	f.mustExec("peer-b", "iptables -A OUTPUT -d "+ipA+" -j DROP")
	time.Sleep(3 * time.Second)
	f.mustExec("peer-a", "iptables -F OUTPUT")
	f.mustExec("peer-b", "iptables -F OUTPUT")

	// Compare hashes once both sides settle.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		hashA, _ := f.execAllowFail("peer-a", `sha256sum /repos/shared/large.bin 2>/dev/null | awk '{print $1}'`)
		hashB, _ := f.execAllowFail("peer-b", `sha256sum /repos/shared/large.bin 2>/dev/null | awk '{print $1}'`)
		if h := strings.TrimSpace(hashA); h != "" && h == strings.TrimSpace(hashB) {
			t.Logf("partition recovery confirmed: hash=%s", h)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("partition recovery timed out; hashes never converged within 120s")
}

// TestCrashMidReconcile sends SIGKILL to the dotkeeper daemon on peer-a during
// an active reconcile pass, restarts it, and asserts the next reconcile
// completes without leaving the config in a half-written state.
func TestCrashMidReconcile(t *testing.T) {
	f := newFixture(t)
	f.pair()

	// Saturate reconcile by adding several files in quick succession then
	// reconciling. Kill mid-pass.
	for i := 0; i < 20; i++ {
		f.writeFile("peer-a", fmt.Sprintf("crash-%02d.txt", i), fmt.Sprintf("v%d\n", i))
	}

	// Background reconcile and kill it after 250ms.
	f.mustExec("peer-a",
		`dotkeeper reconcile > /tmp/reconcile.log 2>&1 &
		echo $! > /tmp/reconcile.pid
		sleep 0.25
		kill -KILL "$(cat /tmp/reconcile.pid)" 2>/dev/null || true`,
	)

	// Now run reconcile cleanly — it should not hang on a stale lock or
	// half-written config.
	out, err := f.exec("peer-a", "dotkeeper reconcile", 60*time.Second)
	if err != nil {
		t.Fatalf("post-crash reconcile failed: %v\n%s", err, out)
	}
	t.Logf("post-crash reconcile output:\n%s", out)
}

// TestPathologicalFilenames creates files with names that historically break
// sync tools: emojis, embedded spaces, leading dots, very long names,
// uppercase/lowercase variants (case-sensitivity trap on macOS hosts).
func TestPathologicalFilenames(t *testing.T) {
	f := newFixture(t)
	f.pair()

	pathological := []struct {
		name     string
		contents string
	}{
		{"with spaces.txt", "spaces\n"},
		{".leading-dot", "hidden\n"},
		{"unicode-📁-folder.txt", "emoji\n"},
		{strings.Repeat("a", 200) + ".txt", "long\n"},
		{"Case-Sensitive.txt", "upper\n"},
		// case-collision intentionally omitted: most filesystems normalize.
	}
	for _, p := range pathological {
		f.writeFile("peer-a", p.name, p.contents)
	}

	for _, p := range pathological {
		if err := f.waitForFile("peer-b", p.name, p.contents, 60*time.Second); err != nil {
			t.Errorf("pathological filename %q failed to propagate: %v", p.name, err)
		} else {
			t.Logf("ok: %q", p.name)
		}
	}
}

// TestManyFilesBurst writes 2000 small files in a tight loop and waits for
// peer-b to mirror them all. The threshold is intentionally aggressive — if
// dotkeeper or Syncthing falls behind under indexing pressure, this is where
// it shows.
func TestManyFilesBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("burst scenario skipped in -short mode")
	}
	f := newFixture(t)
	f.pair()

	f.mustExec("peer-a",
		`mkdir -p /repos/shared/burst
		for i in $(seq 1 2000); do echo "$i" > /repos/shared/burst/$i.txt; done
		ls /repos/shared/burst | wc -l`,
	)

	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := f.execAllowFail("peer-b", `ls /repos/shared/burst 2>/dev/null | wc -l`)
		if strings.TrimSpace(out) == "2000" {
			t.Logf("all 2000 files arrived on peer-b within %s",
				180*time.Second-time.Until(deadline))
			return
		}
		time.Sleep(2 * time.Second)
	}
	tail, _ := f.execAllowFail("peer-b", `ls /repos/shared/burst 2>/dev/null | wc -l`)
	t.Fatalf("burst sync did not complete; peer-b file count: %s", strings.TrimSpace(tail))
}

// TestConcurrentTrackUntrack hammers `dotkeeper track` and `untrack` against
// the same path from a single peer. Surfaces any race in state.toml writes or
// reconcile decisions about a churning repo.
func TestConcurrentTrackUntrack(t *testing.T) {
	f := newFixture(t)
	f.pair()

	out, err := f.exec("peer-a",
		`set -e
		mkdir -p /repos/churn
		cd /repos/churn
		git init -q
		git config user.email "t@t.com"
		git config user.name "t"
		git commit -q --allow-empty -m init
		for i in $(seq 1 20); do
			dotkeeper track /repos/churn &
			dotkeeper untrack /repos/churn &
		done
		wait
		# Final state should be deterministic; final reconcile must succeed.
		dotkeeper reconcile`,
		120*time.Second,
	)
	if err != nil {
		t.Fatalf("concurrent track/untrack left state corrupt: %v\n%s", err, out)
	}
	t.Logf("track/untrack churn survived; final reconcile output:\n%s", out)
}

// TestThreeWayConflict brings up the optional peer-c, pairs it with both
// existing peers, then triggers simultaneous writes on all three sides.
// Verifies dotkeeper's multi-variant conflict handling (cmd/dotkeeper/
// e2e_conflict_test.go: TestCLIConflictMultipleVariants tests the CLI surface;
// this exercises it through a real Syncthing race).
func TestThreeWayConflict(t *testing.T) {
	f := newFixture(t)
	f.composeStart("peer-c", "three-way")

	aID := f.dkInit("peer-a", "peer-a", 0)
	bID := f.dkInit("peer-b", "peer-b", 1)
	cID := f.dkInit("peer-c", "peer-c", 2)

	// Full mesh.
	f.dkPeerAdd("peer-a", "peer-b", bID)
	f.dkPeerAdd("peer-a", "peer-c", cID)
	f.dkPeerAdd("peer-b", "peer-a", aID)
	f.dkPeerAdd("peer-b", "peer-c", cID)
	f.dkPeerAdd("peer-c", "peer-a", aID)
	f.dkPeerAdd("peer-c", "peer-b", bID)
	f.dkTrack("peer-a", "/repos/shared")
	f.dkTrack("peer-b", "/repos/shared")
	f.dkTrack("peer-c", "/repos/shared")
	f.dkStart("peer-a")
	f.dkStart("peer-b")
	f.dkStart("peer-c")
	f.dkReconcile("peer-a")
	f.dkReconcile("peer-b")
	f.dkReconcile("peer-c")

	// Baseline.
	f.writeFile("peer-a", "three-way.md", "baseline\n")
	if err := f.waitForFile("peer-b", "three-way.md", "baseline\n", 60*time.Second); err != nil {
		t.Fatalf("baseline to peer-b: %v", err)
	}
	if err := f.waitForFile("peer-c", "three-way.md", "baseline\n", 60*time.Second); err != nil {
		t.Fatalf("baseline to peer-c: %v", err)
	}

	// Three-way partition. Resolve IPs at runtime.
	ips := map[string]string{
		"peer-a": f.peerIP("peer-a"),
		"peer-b": f.peerIP("peer-b"),
		"peer-c": f.peerIP("peer-c"),
	}
	for _, p := range []string{"peer-a", "peer-b", "peer-c"} {
		for _, dst := range ips {
			f.mustExec(p, "iptables -A OUTPUT -d "+dst+" -j DROP || true")
		}
	}
	f.writeFile("peer-a", "three-way.md", "from-a\n")
	f.writeFile("peer-b", "three-way.md", "from-b\n")
	f.writeFile("peer-c", "three-way.md", "from-c\n")
	time.Sleep(5 * time.Second)
	for _, p := range []string{"peer-a", "peer-b", "peer-c"} {
		f.mustExec(p, "iptables -F OUTPUT")
	}

	// Survey: at least 2 of 3 peers should report sync-conflict files.
	time.Sleep(30 * time.Second)
	var reports []string
	for _, p := range []string{"peer-a", "peer-b", "peer-c"} {
		out, _ := f.execAllowFail(p, `ls /repos/shared | grep three-way.md.sync-conflict || true`)
		if len(strings.TrimSpace(out)) > 0 {
			reports = append(reports, p)
		}
	}
	t.Logf("THREE-WAY CONFLICT — peers reporting conflict files: %v", reports)
	if len(reports) == 0 {
		t.Error("no peer reported a conflict; three-way race did not produce expected divergence")
	}
}

// TestPeerFlap rapidly toggles peer-b online/offline 5 times while peer-a is
// actively writing. Asserts no data loss and convergence after the flap stops.
func TestPeerFlap(t *testing.T) {
	f := newFixture(t)
	f.pair()

	// Background writer on peer-a.
	go func() {
		for i := 0; i < 50; i++ {
			f.writeFile("peer-a", fmt.Sprintf("flap-%02d.txt", i), fmt.Sprintf("v%d\n", i))
			time.Sleep(200 * time.Millisecond)
		}
	}()

	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)
		f.dkStop("peer-b")
		time.Sleep(1 * time.Second)
		f.dkStart("peer-b")
	}

	// Drain: wait for all 50 to arrive.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := f.execAllowFail("peer-b", `ls /repos/shared | grep -c '^flap-' || true`)
		if strings.TrimSpace(out) == "50" {
			t.Log("flap recovery: all 50 files converged on peer-b")
			return
		}
		time.Sleep(2 * time.Second)
	}
	out, _ := f.execAllowFail("peer-b", `ls /repos/shared | grep -c '^flap-' || true`)
	t.Fatalf("flap recovery failed; peer-b has %s of 50 files", strings.TrimSpace(out))
}

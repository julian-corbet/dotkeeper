// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

//go:build multipeer

// Package multipeer drives the two-container e2e fixture defined by
// docker-compose.test.yml. Each TestXxx in this package brings up a fresh
// compose project (so they can run in parallel and clean themselves up), then
// shells dotkeeper subcommands into the containers to script real
// cross-machine sync scenarios.
//
// The harness is opinionated:
//   - Compose project name is derived from the test name + a random suffix.
//   - On test failure, `docker compose logs` is dumped to t.Log before teardown.
//   - All container commands have a deadline; tests fail loudly on timeout.
//   - Helpers panic-via-t.Fatalf instead of returning errors so scenarios stay
//     linear and readable.
package multipeer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fixture owns one compose stack for the duration of one Test*.
type fixture struct {
	t        *testing.T
	project  string // compose project name, used to namespace containers
	repoRoot string // absolute path to dotkeeper repo root (build context)
}

// dkStartTimeout is the outer Go-side cap on `dkStart`'s shell loop. The loop
// itself polls REST for 90s; the Go context gives it a small buffer.
const dkStartTimeout = 120 * time.Second

// newFixture brings up peer-a and peer-b on a fresh compose project and
// registers cleanup. Returns once both containers are running. The harness
// does NOT auto-init or auto-pair the peers — each scenario controls that
// to keep failure modes visible.
//
// Cleanup is registered BEFORE composeUp so that a partial bring-up failure
// (network created, services failed to start) still tears down via t.Cleanup.
// Without this, a failed test would leak its docker network and Docker's IPAM
// would refuse to allocate the next test's subnet.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t:        t,
		project:  uniqueProject(t),
		repoRoot: repoRoot(t),
	}
	t.Cleanup(f.tearDown)
	f.composeUp("peer-a", "peer-b")
	return f
}

func uniqueProject(t *testing.T) string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// Compose project names must be lowercase alnum + dashes.
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, t.Name())
	return fmt.Sprintf("dkmp-%s-%s", safe, hex.EncodeToString(buf[:]))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// runtime.Caller gives us a path inside tests/multipeer/. Walk up to go.mod.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, "ls", filepath.Join(dir, "go.mod")).CombinedOutput()
		cancel()
		if err == nil && strings.Contains(string(out), "go.mod") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root from " + file)
		}
		dir = parent
	}
}

func (f *fixture) composePath() string {
	return filepath.Join(f.repoRoot, "tests", "multipeer", "docker-compose.test.yml")
}

// composeUp brings the listed services to a running state. Build happens here
// on the first call within a CI run; subsequent calls reuse the cached image.
func (f *fixture) composeUp(services ...string) {
	f.t.Helper()
	args := []string{
		"compose",
		"-f", f.composePath(),
		"-p", f.project,
		"up", "-d", "--wait", "--build",
	}
	args = append(args, services...)
	f.runDocker(2*time.Minute, args...)
}

// composeStart brings up a service that's part of a profile (e.g. peer-c).
func (f *fixture) composeStart(service, profile string) {
	f.t.Helper()
	f.runDocker(2*time.Minute,
		"compose", "-f", f.composePath(), "-p", f.project,
		"--profile", profile,
		"up", "-d", "--wait", service,
	)
}

// composeStop halts a single peer (used by offline/online and flap scenarios).
func (f *fixture) composeStop(service string) {
	f.t.Helper()
	f.runDocker(60*time.Second,
		"compose", "-f", f.composePath(), "-p", f.project,
		"stop", service,
	)
}

func (f *fixture) composeStartExisting(service string) {
	f.t.Helper()
	f.runDocker(60*time.Second,
		"compose", "-f", f.composePath(), "-p", f.project,
		"start", service,
	)
}

func (f *fixture) tearDown() {
	if f.t.Failed() {
		// Dump full logs before bringing the stack down so CI postmortems work.
		f.t.Log("--- compose logs (failure dump) ---")
		out, _ := exec.Command("docker", "compose",
			"-f", f.composePath(), "-p", f.project,
			"logs", "--no-color", "--tail=400",
		).CombinedOutput()
		f.t.Log(string(out))
	}
	// Always tear down volumes and network so reruns are clean.
	_ = exec.Command("docker", "compose",
		"-f", f.composePath(), "-p", f.project,
		"down", "-v", "--remove-orphans",
	).Run()
}

// exec runs a shell command inside a peer container and returns combined output.
// Failure (non-zero exit) is a test fatal — scenarios use mustExec for happy paths
// and execAllowFail for adversarial scenarios that EXPECT non-zero exits.
func (f *fixture) exec(peer, sh string, timeout time.Duration) (string, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", "-i",
		f.project+"-"+peer, "sh", "-c", sh,
	).CombinedOutput()
	return string(out), err
}

func (f *fixture) mustExec(peer, sh string) string {
	f.t.Helper()
	out, err := f.exec(peer, sh, 60*time.Second)
	if err != nil {
		f.t.Fatalf("exec %s %q failed: %v\n%s", peer, sh, err, out)
	}
	return out
}

func (f *fixture) execAllowFail(peer, sh string) (string, error) {
	return f.exec(peer, sh, 60*time.Second)
}

// runDocker is a typed wrapper around `docker` for compose lifecycle calls.
func (f *fixture) runDocker(timeout time.Duration, args ...string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("docker %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// --- dotkeeper-level helpers ---------------------------------------------

// dkInit runs `dotkeeper init` and returns the device ID parsed from stdout.
// The init command prints a line like:
//
//	Syncthing device ID: ABCDEFG-...
func (f *fixture) dkInit(peer, name string, slot int) string {
	f.t.Helper()
	out := f.mustExec(peer, fmt.Sprintf(
		"mkdir -p /home/dk/.config /home/dk/.local/state && dotkeeper init --name %s --slot %d",
		shellQuote(name), slot,
	))
	id := parseDeviceID(out)
	if id == "" {
		f.t.Fatalf("could not parse device ID from init output on %s:\n%s", peer, out)
	}
	return id
}

func parseDeviceID(initOutput string) string {
	for _, line := range strings.Split(initOutput, "\n") {
		// Match the literal label printed by `dotkeeper init`. The exact string
		// is asserted by cmd/dotkeeper/e2e_test.go: TestE2EInitStatus expects
		// "device ID" in the output.
		if idx := strings.Index(line, "device ID"); idx >= 0 {
			tail := line[idx+len("device ID"):]
			tail = strings.TrimLeft(tail, ":= \t")
			tail = strings.TrimSpace(tail)
			// Device IDs are 56 chars, base32, dash-separated into groups of 7.
			if len(tail) >= 56 {
				return strings.SplitN(tail, " ", 2)[0]
			}
		}
	}
	return ""
}

// dkPeerAdd registers a peer in this machine's state.toml.
func (f *fixture) dkPeerAdd(peer, otherName, otherDeviceID string) {
	f.t.Helper()
	f.mustExec(peer, fmt.Sprintf("dotkeeper peer add %s %s",
		shellQuote(otherName), shellQuote(otherDeviceID),
	))
}

// dkTrack initializes a git repo at path inside the peer and tracks it.
// Both peers tracking the SAME path string produces the SAME folder ID
// (dk-<basename>-<sha256[:8]>) so Syncthing recognizes the shared folder.
func (f *fixture) dkTrack(peer, path string) {
	f.t.Helper()
	f.mustExec(peer, fmt.Sprintf(
		`set -e
		mkdir -p %[1]s
		cd %[1]s
		if [ ! -d .git ]; then
			git init -q
			git config user.email "test@dotkeeper.dev"
			git config user.name "test"
			git commit -q --allow-empty -m "init"
		fi
		dotkeeper track %[1]s`,
		shellQuote(path),
	))
}

// dkStart launches the daemon in the background and waits for the Syncthing
// REST API to respond. Returns once the peer is ready to accept connections.
func (f *fixture) dkStart(peer string) {
	f.t.Helper()
	// dotkeeper's embedded Syncthing binds REST to 127.0.0.1:18384 (not the
	// upstream default 8384) — see internal/stclient/client.go: APIAddress.
	// The daemon also needs auth, but /rest/noauth/health is unauthenticated
	// and sufficient for a liveness probe.
	//
	// 90 iterations × 1s gives 90s — generous because cold-start on the
	// runner sometimes spends several seconds on first-time identity scrub.
	// The outer Go-side timeout is bumped to 120s via dkStartTimeout below.
	out, err := f.exec(peer,
		`nohup dotkeeper start >/tmp/dotkeeper.log 2>&1 &
		echo $! > /tmp/dotkeeper.pid
		for i in $(seq 1 90); do
			if curl -sf -o /dev/null http://127.0.0.1:18384/rest/noauth/health 2>/dev/null \
				|| curl -sf -o /dev/null http://127.0.0.1:18384/rest/system/ping 2>/dev/null; then
				exit 0
			fi
			# Bail early if dotkeeper itself exited (otherwise we waste 90s).
			if ! kill -0 "$(cat /tmp/dotkeeper.pid)" 2>/dev/null; then
				echo "dotkeeper exited before REST came up; full log:"
				cat /tmp/dotkeeper.log
				exit 2
			fi
			sleep 1
		done
		echo "Syncthing REST never came up after 90s; last log:"
		tail -80 /tmp/dotkeeper.log
		exit 1`,
		dkStartTimeout,
	)
	if err != nil {
		f.t.Fatalf("dkStart on %s failed: %v\n%s", peer, err, out)
	}
}

// dkStop ends the daemon. Used by offline/online scenarios.
func (f *fixture) dkStop(peer string) {
	f.t.Helper()
	f.execAllowFail(peer,
		`if [ -f /tmp/dotkeeper.pid ]; then
			kill -TERM "$(cat /tmp/dotkeeper.pid)" 2>/dev/null || true
			# Give it 5s to shut down cleanly.
			for i in 1 2 3 4 5; do
				kill -0 "$(cat /tmp/dotkeeper.pid)" 2>/dev/null || exit 0
				sleep 1
			done
			kill -KILL "$(cat /tmp/dotkeeper.pid)" 2>/dev/null || true
		fi`,
	)
}

// dkReconcile forces one reconciliation pass and returns its output.
func (f *fixture) dkReconcile(peer string) string {
	f.t.Helper()
	return f.mustExec(peer, "dotkeeper reconcile")
}

// writeFile creates a file inside the shared repo on the given peer.
func (f *fixture) writeFile(peer, relpath, contents string) {
	f.t.Helper()
	f.mustExec(peer, fmt.Sprintf(
		`mkdir -p "$(dirname /repos/shared/%[1]s)" && printf '%%s' %[2]s > /repos/shared/%[1]s`,
		relpath, shellQuote(contents),
	))
}

// waitForFile polls for a file's existence on the given peer with given
// expected contents. Returns nil on match, error on timeout or mismatch.
func (f *fixture) waitForFile(peer, relpath, expectContents string, timeout time.Duration) error {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	var lastSeen string
	for time.Now().Before(deadline) {
		out, err := f.execAllowFail(peer, fmt.Sprintf(
			`if [ -f /repos/shared/%[1]s ]; then cat /repos/shared/%[1]s; else echo __ABSENT__; fi`,
			relpath,
		))
		if err == nil {
			lastSeen = strings.TrimRight(out, "\n")
			if lastSeen == expectContents {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s on %s; last seen: %q (want %q)",
		relpath, peer, lastSeen, expectContents)
}

// pair runs the full init + cross-peer add + reconcile + start dance for
// the two-peer happy path. After it returns, both peers know each other and
// have Syncthing running. Most scenarios call this then proceed to track +
// write + assert.
func (f *fixture) pair() (aID, bID string) {
	f.t.Helper()
	aID = f.dkInit("peer-a", "peer-a", 0)
	bID = f.dkInit("peer-b", "peer-b", 1)
	f.dkPeerAdd("peer-a", "peer-b", bID)
	f.dkPeerAdd("peer-b", "peer-a", aID)
	// Track the shared repo on both sides so folder IDs match.
	f.dkTrack("peer-a", "/repos/shared")
	f.dkTrack("peer-b", "/repos/shared")
	f.dkStart("peer-a")
	f.dkStart("peer-b")
	// Reconcile after start so Syncthing has REST available for config writes.
	f.dkReconcile("peer-a")
	f.dkReconcile("peer-b")
	return aID, bID
}

// peerIP resolves the IP address Docker assigned to the given peer on this
// fixture's bridge network. Used by adversarial scenarios that need iptables
// rules targeting specific peers (we can no longer hard-code 10.42.0.10/.11
// because each compose project gets a Docker-assigned subnet).
func (f *fixture) peerIP(peer string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// `{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}` works because
	// each container is attached to exactly one network in our compose setup.
	out, err := exec.CommandContext(ctx, "docker", "inspect",
		"-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		f.project+"-"+peer,
	).CombinedOutput()
	if err != nil {
		f.t.Fatalf("docker inspect %s: %v\n%s", peer, err, out)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		f.t.Fatalf("docker inspect %s returned no IP", peer)
	}
	return ip
}

// shellQuote returns a single-quoted shell literal. Used for paths and names
// that we pass into `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

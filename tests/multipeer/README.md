# Multipeer e2e tests

Two-container Syncthing fixture for testing dotkeeper's cross-machine sync
behavior end-to-end. The existing in-process Go tests under `cmd/dotkeeper/`
and `internal/*/` exercise units, the CLI surface, and fabricated conflict
files; this suite exercises **two real Syncthing peers actually exchanging
blocks across a network**.

## Why a separate suite?

`go test ./...` (the default CI run) cannot easily spin up two interlocking
daemons with distinct device IDs and a shared network. This suite is gated
behind the `multipeer` build tag so it only runs when explicitly requested,
keeping the fast inner-loop unaffected.

## Running locally

```sh
# Build the test image once. Subsequent runs reuse the cached layers.
docker build -f tests/multipeer/Dockerfile.test -t dotkeeper-multipeer-test:local .

# Run the whole suite.
go test -tags multipeer -v -timeout 25m ./tests/multipeer/...

# Run a single scenario.
go test -tags multipeer -v -run TestPropagateAtoB ./tests/multipeer/...

# Run only the adversarial scenarios.
go test -tags multipeer -v -run '^Test(ClockSkew|NetworkPartition|Crash|Pathological|ManyFiles|ConcurrentTrack|ThreeWay|PeerFlap)' ./tests/multipeer/...
```

## What's in here

| File | Role |
|------|------|
| `Dockerfile.test` | Builds dotkeeper from the working tree into a small Alpine runtime with git, iptables, libfaketime, curl, jq |
| `docker-compose.test.yml` | Two-peer (plus optional peer-c) topology on a private bridge network |
| `harness.go` | Compose lifecycle + per-peer `docker exec` helpers + dotkeeper command wrappers + sync-quiescence waiters |
| `happy_test.go` | 5 BDD scenarios for the canonical paths |
| `adversarial_test.go` | 8 scenarios that try to trip dotkeeper up |

## Scenarios

**Happy path** (`happy_test.go`):

1. `TestPropagateAtoB` — file written on peer-a appears on peer-b.
2. `TestPropagateBtoA` — symmetric (catches asymmetric pair-add bugs).
3. `TestConflictRoundTrip` — both peers edit during partition, heal, expect
   `.sync-conflict-*` files, `dotkeeper conflict resolve-all` cleans up.
4. `TestOfflineCatchUp` — peer-b fully stopped, peer-a mutates, peer-b
   restarts and catches up.
5. `TestTrackAfterPair` — adding/re-reconciling a tracked repo while the
   daemon is running.

**Adversarial** (`adversarial_test.go`):

1. `TestClockSkew` — peer-b's clock is 1h behind via libfaketime; documents
   how dotkeeper's modtime-based conflict heuristic behaves.
2. `TestNetworkPartitionMidSync` — 8 MB transfer interrupted mid-stream by
   a bridge drop; asserts hash-equality after heal.
3. `TestCrashMidReconcile` — SIGKILL the daemon during reconcile; the next
   reconcile must complete cleanly.
4. `TestPathologicalFilenames` — spaces, leading dot, emoji, 200-char names,
   uppercase variants.
5. `TestManyFilesBurst` — 2000 small files in one shot; convergence within
   180s.
6. `TestConcurrentTrackUntrack` — 20 concurrent `track`/`untrack` races on
   the same repo; final reconcile must succeed.
7. `TestThreeWayConflict` — peer-c joins; three-way partition + simultaneous
   write produces conflict files on at least 2 of 3 peers.
8. `TestPeerFlap` — peer-b toggles online/offline 5× while peer-a writes 50
   files; all 50 must converge.

## Adversarial scenarios are informational

Failures in `adversarial_test.go` are not always regressions — sometimes they
surface a real weakness in how dotkeeper handles a hostile environment. Read
the `t.Logf` output: tests use it deliberately to capture what was observed
rather than what was demanded.

## Compose project naming

Each test mints a unique compose project name (`dkmp-<test>-<hex>`), so:

- Tests can run in parallel without volume/network collisions.
- A panic that skips `t.Cleanup` leaves an inspectable stack named
  predictably; the CI workflow's `Prune leftover compose projects` step
  reaps anything that wasn't torn down.

## Folder ID requirement

dotkeeper computes folder IDs deterministically: `dk-<basename>-<sha256[:8]>`
of either the git remote origin or (fallback) the absolute path string. The
harness exploits this: both peers track `/repos/shared` (same path string,
no git remote), so they compute identical folder IDs and Syncthing can match
them. Scenarios that need different folder IDs would have to set explicit
`syncthing_folder_id` values via `.dotkeeper.toml` instead.

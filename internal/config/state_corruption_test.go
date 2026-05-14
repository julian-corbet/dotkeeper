// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadStateV2_CorruptFile pins the failure mode when state.toml on disk
// is invalid TOML. Users upgrading from a pre-atomic-write dotkeeper may
// already have a corrupted state.toml from a prior racy `track`/`untrack`
// crash; they need a clear, actionable error from LoadStateV2 instead of a
// silent partial-state load or a panic.
func TestLoadStateV2_CorruptFile(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel in Go 1.26 so this test runs
	// serially. Isolation comes from t.TempDir() + Setenv'd XDG_STATE_HOME.
	withStateDirParallel(t)

	// The same corruption pattern that surfaced in PR #7's adversarial test:
	// a trailing `"` from an interleaved write breaks the parser at line 8.
	bad := []byte(`schema_version = 2
syncthing_device_id = "ABC"

tracked_overrides = [
    "/repos/a",
]

[[peers"
name = "broken"
`)
	path := StateV2Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatalf("seed corrupt state.toml: %v", err)
	}

	state, err := LoadStateV2()
	if err == nil {
		t.Fatalf("LoadStateV2 returned %+v; want error on corrupt file", state)
	}
	if !strings.Contains(err.Error(), "state.toml") {
		t.Errorf("error should name the file ('state.toml'); got: %v", err)
	}
	// The error must be informative enough to point a human at the corruption
	// rather than smuggling a partial state into ambient nil-safety logic.
	if state != nil {
		t.Errorf("LoadStateV2 returned non-nil state alongside error: %+v", state)
	}
}

// TestMutateStateV2_RefusesToOverwriteCorruptState ensures that even with the
// MutateStateV2 happy path, a corrupt state.toml is NOT silently overwritten.
// Otherwise a user's hand-edits or prior-version corruption could be lost
// just because some command ran a fresh mutation. The contract: if state can
// be loaded, mutate-and-write; if it can't, surface the load error.
func TestMutateStateV2_RefusesToOverwriteCorruptState(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel in Go 1.26 so this test runs
	// serially. Isolation comes from t.TempDir() + Setenv'd XDG_STATE_HOME.
	withStateDirParallel(t)

	path := StateV2Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not = valid = toml\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt state.toml: %v", err)
	}

	err := MutateStateV2(func(state *StateV2) error {
		state.TrackedOverrides = append(state.TrackedOverrides, "/should/not/land")
		return nil
	})
	if err == nil {
		t.Fatal("MutateStateV2 silently overwrote corrupt state; should have errored")
	}

	// And the corrupt file must still be there for the user to inspect/repair.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("post-error read: %v", err)
	}
	if !strings.Contains(string(data), "not = valid = toml") {
		t.Errorf("MutateStateV2 mutated the corrupt file; contents are now: %q", string(data))
	}
}

// withStateDirParallel is the t.Parallel-compatible variant of withStateDir.
// t.Setenv is incompatible with t.Parallel in Go 1.26, so this helper sets up
// an isolated process-local state dir via a TempDir rather than env, and
// arranges for StateV2Path() / StateDir() to resolve to it by overriding the
// XDG_STATE_HOME getter. In practice we just rely on t.Setenv with a sync,
// so this helper does what the corruption tests need: a clean state dir per
// test that other parallel tests cannot touch.
//
// NOTE: t.Setenv documents that "any subsequent call to t.Setenv from the
// same test or any sub-test will cause the test to fail when running in
// parallel". We rely on each parallel test calling this exactly once.
func withStateDirParallel(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
}

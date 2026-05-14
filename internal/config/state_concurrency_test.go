// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestMutateStateV2_InProcessConcurrency proves that N goroutines hammering
// MutateStateV2 against the same state.toml never corrupt the file and never
// lose updates. Each goroutine appends its own marker peer; the final state
// must contain all N markers and parse as valid TOML.
//
// This is the in-process companion to the cross-process test below — the
// in-process case exercises flock semantics within a single binary (each
// goroutine opens its own lock fd).
func TestMutateStateV2_InProcessConcurrency(t *testing.T) {
	withStateDir(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			err := MutateStateV2(func(state *StateV2) error {
				state.TrackedOverrides = append(state.TrackedOverrides,
					fmt.Sprintf("/repos/peer-%02d", i))
				return nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent mutate failed: %v", err)
	}

	final, err := LoadStateV2()
	if err != nil {
		t.Fatalf("post-mutation load failed (TOML corrupt?): %v", err)
	}
	if final == nil {
		t.Fatal("post-mutation state.toml does not exist")
	}
	if got := len(final.TrackedOverrides); got != n {
		t.Errorf("TrackedOverrides count = %d, want %d (lost updates: %v)",
			got, n, missingMarkers(n, final.TrackedOverrides))
	}
}

// TestMutateStateV2_CrossProcessConcurrency forks N copies of the test binary
// (via go test -run), each of which appends one marker to state.toml. The
// state.toml must remain valid TOML and contain all N markers afterwards.
// Cross-process is the case that actually matters in production: separate
// `dotkeeper track`/`untrack` invocations from the user's shell.
func TestMutateStateV2_CrossProcessConcurrency(t *testing.T) {
	if os.Getenv("DK_STATE_CHILD") != "" {
		// Child: append one marker, then exit.
		i := os.Getenv("DK_STATE_CHILD")
		if err := MutateStateV2(func(state *StateV2) error {
			state.TrackedOverrides = append(state.TrackedOverrides,
				fmt.Sprintf("/repos/child-%s", i))
			return nil
		}); err != nil {
			fmt.Fprintln(os.Stderr, "child mutate:", err)
			os.Exit(2)
		}
		return
	}

	dir := withStateDir(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	failures := make(chan string, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe, "-test.run=^TestMutateStateV2_CrossProcessConcurrency$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(),
				"DK_STATE_CHILD="+fmt.Sprintf("%02d", i),
				"XDG_STATE_HOME="+filepath.Dir(dir),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				failures <- fmt.Sprintf("child %d: %v\n%s", i, err, out)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Error(f)
	}

	final, err := LoadStateV2()
	if err != nil {
		t.Fatalf("post-fork load failed (TOML corrupt?): %v", err)
	}
	if got := len(final.TrackedOverrides); got != n {
		t.Errorf("TrackedOverrides count = %d, want %d", got, n)
	}
}

// withStateDir points XDG_STATE_HOME at a fresh t.TempDir() and returns the
// resolved state.toml directory. Cleans up automatically.
func withStateDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	return filepath.Join(root, "dotkeeper")
}

// missingMarkers returns the set of expected peer-NN strings that aren't
// present in the observed list. Used by error messages to show exactly which
// updates were lost rather than just a count.
func missingMarkers(n int, got []string) []string {
	have := make(map[string]bool, len(got))
	for _, p := range got {
		have[p] = true
	}
	var missing []string
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("/repos/peer-%02d", i)
		if !have[want] {
			missing = append(missing, want)
		}
	}
	return missing
}

func init() {
	// In the child-process branch we want a minimal go-test runtime; suppress
	// any stray output from t.Helper-style harness chatter that would confuse
	// the parent's CombinedOutput check.
	if os.Getenv("DK_STATE_CHILD") != "" && !strings.Contains(strings.Join(os.Args, " "), "-test.run") {
		// Defensive: shouldn't happen, but exit cleanly if invoked oddly.
		runtime.Goexit()
	}
}

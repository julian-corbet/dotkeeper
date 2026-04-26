// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func stubLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestReconcilerEndToEnd(t *testing.T) {
	t.Parallel()

	desired := Desired{
		Repos: map[string]RepoDesired{
			"/dots": {
				Path:              "/dots",
				SyncthingFolderID: "dk-dots",
				ShareWith:         []string{"PEER-1"},
			},
		},
	}
	observed := Observed{
		// Folder exists but device list differs — should produce UpdateSyncthingFolderDevices.
		ManagedFolders: []FolderObs{
			{SyncthingFolderID: "dk-dots", Path: "/dots", Devices: []string{"PEER-OLD"}},
		},
		// Dirty repo — should produce GitCommitDirty.
		TrackedRepos: []RepoObs{
			{Path: "/dots", IsDirty: true, HeadCommit: "abc"},
		},
	}

	stub := &StubApplier{}
	r := &Reconciler{
		Desired:  func(_ context.Context) (Desired, error) { return desired, nil },
		Observed: func(_ context.Context) (Observed, error) { return observed, nil },
		Applier:  stub,
		Logger:   stubLogger(),
	}

	plan, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("expected non-empty plan")
	}
	if len(stub.Applied) != len(plan) {
		t.Fatalf("applied %d actions, plan has %d", len(stub.Applied), len(plan))
	}
}

func TestReconcilerContinuesOnError(t *testing.T) {
	t.Parallel()

	observed := Observed{
		TrackedRepos: []RepoObs{
			{Path: "/a", IsDirty: true},
			{Path: "/b", IsDirty: true},
		},
	}

	firstAction := GitCommitDirty{RepoPath: "/a"}
	sentinel := errors.New("commit failed")
	stub := &StubApplier{
		Errors: map[string]error{
			firstAction.Describe(): sentinel,
		},
	}

	r := &Reconciler{
		Desired:  func(_ context.Context) (Desired, error) { return Desired{}, nil },
		Observed: func(_ context.Context) (Observed, error) { return observed, nil },
		Applier:  stub,
		Logger:   stubLogger(),
	}

	plan, err := r.Reconcile(context.Background())
	// Should return the first error but still attempt all actions.
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	// Both actions must have been attempted despite the first one failing.
	if len(stub.Applied) != len(plan) {
		t.Fatalf("expected all %d actions attempted, got %d", len(plan), len(stub.Applied))
	}
}

func TestReconcilerDesiredProviderError(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("config read failure")
	r := &Reconciler{
		Desired:  func(_ context.Context) (Desired, error) { return Desired{}, providerErr },
		Observed: func(_ context.Context) (Observed, error) { return Observed{}, nil },
		Applier:  &StubApplier{},
		Logger:   stubLogger(),
	}

	_, err := r.Reconcile(context.Background())
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected provider error, got: %v", err)
	}
}

func TestReconcilerObservedProviderError(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("syncthing unreachable")
	r := &Reconciler{
		Desired:  func(_ context.Context) (Desired, error) { return Desired{}, nil },
		Observed: func(_ context.Context) (Observed, error) { return Observed{}, providerErr },
		Applier:  &StubApplier{},
		Logger:   stubLogger(),
	}

	_, err := r.Reconcile(context.Background())
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected provider error, got: %v", err)
	}
}

func TestReconcilerNilLoggerUsesDefault(t *testing.T) {
	t.Parallel()

	// Nil Logger must not panic.
	r := &Reconciler{
		Desired:  func(_ context.Context) (Desired, error) { return Desired{}, nil },
		Observed: func(_ context.Context) (Observed, error) { return Observed{}, nil },
		Applier:  &StubApplier{},
		Logger:   nil,
	}

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

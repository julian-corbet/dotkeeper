// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"context"
	"errors"
	"testing"
)

func TestStubApplierRecords(t *testing.T) {
	t.Parallel()

	stub := &StubApplier{}
	ctx := context.Background()

	actions := []Action{
		AddSyncthingFolder{FolderID: "dk-a", Path: "/a", Devices: []string{"DEV-1"}},
		GitCommitDirty{RepoPath: "/repo", Message: "auto: repo 2026-01-01T00:00:00Z"},
		GitPushRepo{RepoPath: "/repo"},
	}

	for _, a := range actions {
		if err := stub.Apply(ctx, a); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(stub.Applied) != len(actions) {
		t.Fatalf("expected %d applied actions, got %d", len(actions), len(stub.Applied))
	}
	for i, a := range actions {
		if stub.Applied[i].Describe() != a.Describe() {
			t.Errorf("applied[%d] = %q, want %q", i, stub.Applied[i].Describe(), a.Describe())
		}
	}
}

func TestStubApplierErrorInjection(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("simulated push failure")
	pushAction := GitPushRepo{RepoPath: "/repo"}

	stub := &StubApplier{
		Errors: map[string]error{
			pushAction.Describe(): sentinel,
		},
	}
	ctx := context.Background()

	// Action without configured error returns nil.
	addAction := AddSyncthingFolder{FolderID: "dk-x", Path: "/x"}
	if err := stub.Apply(ctx, addAction); err != nil {
		t.Fatalf("expected nil error for add action, got: %v", err)
	}

	// Action with configured error returns that error.
	err := stub.Apply(ctx, pushAction)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}

	// Both actions were still recorded.
	if len(stub.Applied) != 2 {
		t.Fatalf("expected 2 applied actions, got %d", len(stub.Applied))
	}
}

func TestStubApplierNilErrors(t *testing.T) {
	t.Parallel()

	// Nil Errors map must not panic.
	stub := &StubApplier{}
	ctx := context.Background()
	action := RemoveSyncthingFolder{FolderID: "dk-gone"}
	if err := stub.Apply(ctx, action); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.Applied) != 1 {
		t.Fatalf("expected 1 applied action, got %d", len(stub.Applied))
	}
}

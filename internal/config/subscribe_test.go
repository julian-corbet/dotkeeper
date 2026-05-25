// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"testing"
)

// TestMergeSubscriptionsDeclarativeWins pins the precedence
// invariant: when the same identity exists in both
// machine.toml and state.toml, the machine.toml entry takes
// precedence. Without this rule, an imperatively-added
// subscription could silently override the operator's
// declarative config, defeating the point of having a flake.
func TestMergeSubscriptionsDeclarativeWins(t *testing.T) {
	decl := []SubscriptionEntry{
		{Canonical: "github.com/foo/rag", Path: "/decl/path"},
	}
	imp := []SubscriptionEntry{
		{Canonical: "github.com/foo/rag", Path: "/imp/path"},
	}
	out := MergeSubscriptions(decl, imp)
	if len(out) != 1 {
		t.Fatalf("expected 1 entry after dedup, got %d", len(out))
	}
	if out[0].Path != "/decl/path" {
		t.Errorf("declarative should win on conflict; got Path=%q", out[0].Path)
	}
}

func TestMergeSubscriptionsUnion(t *testing.T) {
	decl := []SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	imp := []SubscriptionEntry{
		{Canonical: "github.com/foo/cv"},
		{Name: "dotfiles"}, // non-git, name-keyed
	}
	out := MergeSubscriptions(decl, imp)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(out), out)
	}
	// First entry should be the declarative one (order preserved).
	if out[0].Canonical != "github.com/foo/rag" {
		t.Errorf("first entry should be declarative; got %+v", out[0])
	}
}

// TestMergeSubscriptionsCanonicalAndNameAreDistinct — a Canonical
// of "rag" and a Name of "rag" must not dedup against each other.
// They live in different identity namespaces.
func TestMergeSubscriptionsCanonicalAndNameAreDistinct(t *testing.T) {
	out := MergeSubscriptions(
		[]SubscriptionEntry{{Canonical: "rag"}},
		[]SubscriptionEntry{{Name: "rag"}},
	)
	if len(out) != 2 {
		t.Errorf("Canonical 'rag' and Name 'rag' should be distinct; got %d entries", len(out))
	}
}

// TestMergeSubscriptionsSkipsBlank — entries with neither
// Canonical nor Name (zero value) are silently dropped rather
// than included. Defensive — callers should validate before
// passing in, but a stray zero entry shouldn't crash the merge.
func TestMergeSubscriptionsSkipsBlank(t *testing.T) {
	out := MergeSubscriptions(
		[]SubscriptionEntry{{}, {Canonical: "github.com/foo/rag"}},
		nil,
	)
	if len(out) != 1 {
		t.Errorf("blank entry should be skipped; got %d entries", len(out))
	}
}

// TestMergeSubscriptionsIntraSliceDedup — two entries with the
// same key inside the SAME slice still dedup (first wins).
// Catches the case where Nix generates the same subscription
// twice from overlapping templates.
func TestMergeSubscriptionsIntraSliceDedup(t *testing.T) {
	decl := []SubscriptionEntry{
		{Canonical: "github.com/foo/rag", Path: "/first"},
		{Canonical: "github.com/foo/rag", Path: "/second"},
	}
	out := MergeSubscriptions(decl, nil)
	if len(out) != 1 {
		t.Fatalf("intra-slice duplicate should dedup; got %d", len(out))
	}
	if out[0].Path != "/first" {
		t.Errorf("first occurrence should win; got Path=%q", out[0].Path)
	}
}

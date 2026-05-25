// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package subscribe

import (
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

func TestResolveAcceptsMatchingOffer(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	offers := []Offer{
		{FolderID: "dk-rag-abc", Label: "github.com/foo/rag",
			FromPeerName: "desktop", FromDeviceID: "PEER-A"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 1 {
		t.Fatalf("expected 1 acceptance, got %d", len(r.Acceptances))
	}
	if r.Acceptances[0].Offer.FromPeerName != "desktop" {
		t.Errorf("wrong peer in acceptance: %+v", r.Acceptances[0])
	}
	if r.Statuses[0].Status != StatusAccepted {
		t.Errorf("status = %v, want accepted", r.Statuses[0].Status)
	}
}

// TestResolveSkipsAlreadyLocal — the local Syncthing already has
// this folder ID. We don't re-accept (would churn); we report
// the subscription as "pending" because nothing actionable
// remains.
func TestResolveSkipsAlreadyLocal(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	offers := []Offer{
		{FolderID: "dk-rag-abc", Label: "github.com/foo/rag",
			FromPeerName: "desktop", FromDeviceID: "PEER-A"},
	}
	r := Resolve(subs, offers, map[string]bool{"dk-rag-abc": true})
	if len(r.Acceptances) != 0 {
		t.Errorf("expected 0 acceptances when folder already local, got %d", len(r.Acceptances))
	}
}

// TestResolvePendingWhenNoOffers — subscription is declared but
// no peer is currently offering the folder. Status pending; no
// action yet. Next reconcile (after a peer comes online with the
// folder) will switch to accepted.
func TestResolvePendingWhenNoOffers(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	r := Resolve(subs, nil, nil)
	if len(r.Acceptances) != 0 {
		t.Errorf("expected 0 acceptances with no offers, got %d", len(r.Acceptances))
	}
	if r.Statuses[0].Status != StatusPending {
		t.Errorf("status = %v, want pending", r.Statuses[0].Status)
	}
}

// TestResolveAmbiguousOnMultipleFolderIDs — two different
// publishers offered the same canonical URL but assigned
// different folder IDs (a fork situation, or two independent
// tracks of the same repo). Operator must intervene; we don't
// pick.
func TestResolveAmbiguousOnMultipleFolderIDs(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	offers := []Offer{
		{FolderID: "dk-rag-aaa", Label: "github.com/foo/rag",
			FromPeerName: "desktop", FromDeviceID: "PEER-A"},
		{FolderID: "dk-rag-bbb", Label: "github.com/foo/rag",
			FromPeerName: "laptop", FromDeviceID: "PEER-B"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 0 {
		t.Errorf("ambiguous: expected 0 acceptances, got %d", len(r.Acceptances))
	}
	if r.Statuses[0].Status != StatusAmbiguous {
		t.Errorf("status = %v, want ambiguous", r.Statuses[0].Status)
	}
	if len(r.Statuses[0].Offers) != 2 {
		t.Errorf("ambiguous status should list both offers; got %d", len(r.Statuses[0].Offers))
	}
}

// TestResolveSameFolderMultiplePeers — same folder ID offered
// by two peers. Pick first peer; sync proceeds. NOT ambiguous.
func TestResolveSameFolderMultiplePeers(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	offers := []Offer{
		{FolderID: "dk-rag-abc", Label: "github.com/foo/rag",
			FromPeerName: "desktop", FromDeviceID: "PEER-A"},
		{FolderID: "dk-rag-abc", Label: "github.com/foo/rag",
			FromPeerName: "laptop", FromDeviceID: "PEER-B"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 1 {
		t.Errorf("expected 1 acceptance (pick first peer); got %d", len(r.Acceptances))
	}
}

// TestResolveNameBasedNonGit — non-git folder uses the `dk:<name>`
// convention.
func TestResolveNameBasedNonGit(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Name: "dotfiles"},
	}
	offers := []Offer{
		{FolderID: "dk-dotfiles-xyz", Label: "dk:dotfiles",
			FromPeerName: "desktop", FromDeviceID: "PEER-A"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 1 {
		t.Fatalf("expected 1 acceptance for name-based match; got %d", len(r.Acceptances))
	}
}

// TestResolveMixedSubscriptions — a realistic operator with both
// git and non-git subscriptions. All resolve independently.
func TestResolveMixedSubscriptions(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
		{Canonical: "github.com/foo/cv"},
		{Name: "dotfiles"},
		{Canonical: "github.com/foo/never-offered"}, // pending
	}
	offers := []Offer{
		{FolderID: "dk-rag-abc", Label: "github.com/foo/rag", FromPeerName: "desktop"},
		{FolderID: "dk-cv-def", Label: "github.com/foo/cv", FromPeerName: "desktop"},
		{FolderID: "dk-dotfiles-xyz", Label: "dk:dotfiles", FromPeerName: "laptop"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 3 {
		t.Errorf("expected 3 acceptances, got %d", len(r.Acceptances))
	}
	pending := 0
	for _, s := range r.Statuses {
		if s.Status == StatusPending {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("expected 1 pending (never-offered), got %d", pending)
	}
}

// TestResolveEmptyLabelOfferIgnored — an offer with no label (a
// non-dotkeeper Syncthing peer somehow on the mesh) doesn't
// participate in matching.
func TestResolveEmptyLabelOfferIgnored(t *testing.T) {
	subs := []config.SubscriptionEntry{
		{Canonical: "github.com/foo/rag"},
	}
	offers := []Offer{
		{FolderID: "stuff", Label: "", FromPeerName: "stranger"},
	}
	r := Resolve(subs, offers, nil)
	if len(r.Acceptances) != 0 {
		t.Errorf("expected unlabeled offer to be ignored; got acceptance")
	}
}

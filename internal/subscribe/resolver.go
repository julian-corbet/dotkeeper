// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package subscribe implements the subscription-matching engine.
// Given a list of declared subscriptions and a set of folders
// peers have offered, the resolver computes which subscriptions
// to accept now, which ones are pending peer availability, and
// which ones have ambiguous offers (multiple peers offering the
// same canonical from different folder IDs).
//
// The package is pure: no I/O, no network, no filesystem. All
// inputs are values; outputs are values. This makes the matching
// logic exhaustively testable and lets the reconciler call into
// it from its diff phase without taking on a transitive
// dependency on Syncthing or the FS.
package subscribe

import (
	"github.com/julian-corbet/dotkeeper/internal/config"
)

// Offer is one folder offered by one peer. The resolver's caller
// flattens the Syncthing pending-folders API + already-shared
// folders into this shape before calling Resolve.
type Offer struct {
	// FolderID is the Syncthing folder ID the offering peer
	// uses for this folder. Same across peers when the folder
	// originated from one publisher and gossiped through; can
	// differ when two operators independently track the same
	// upstream repo with different name-hashes.
	FolderID string

	// Label is what the offering peer set on the folder. For
	// dotkeeper v1.2+ peers this is the canonical git-remote
	// URL. Subscriptions match against this. Empty for non-
	// dotkeeper Syncthing instances (would never appear on a
	// pure-dotkeeper mesh, but tolerated).
	Label string

	// FromPeerName is the human-readable peer name (resolved
	// from device ID by the caller using the machine.toml peer
	// roster). Operators see this in the offers UI.
	FromPeerName string

	// FromDeviceID is the offering peer's Syncthing device ID.
	// Needed by the reconciler to add the peer to the new
	// folder's share-list.
	FromDeviceID string
}

// Acceptance is a resolved subscription → offer match. The
// reconciler turns each Acceptance into an `AcceptSubscription`
// action: create the local path, optionally clone the git remote,
// add the Syncthing folder, share with the offering peer.
type Acceptance struct {
	// Subscription is the operator-declared entry that matched.
	Subscription config.SubscriptionEntry
	// Offer is the peer-side advertisement that matched.
	Offer Offer
}

// SubscriptionStatus categorises each declared subscription for
// operator visibility. The reconciler emits action only for
// Acceptances; the other statuses are surfaced in `dotkeeper
// health` so operators can see WHY a subscription isn't syncing.
type SubscriptionStatus int

const (
	// StatusAccepted: matched to an offer, action will be emitted.
	StatusAccepted SubscriptionStatus = iota
	// StatusPending: subscription is declared but no peer is
	// currently offering a matching folder. Resolves itself when
	// a paired peer eventually advertises the folder.
	StatusPending
	// StatusAmbiguous: multiple distinct folder IDs match the
	// same canonical. Operator must investigate (likely two
	// publishers diverged on naming). Reconciler refuses to
	// auto-accept rather than picking arbitrarily.
	StatusAmbiguous
)

func (s SubscriptionStatus) String() string {
	switch s {
	case StatusAccepted:
		return "accepted"
	case StatusPending:
		return "pending"
	case StatusAmbiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// StatusEntry is the per-subscription resolution outcome,
// produced alongside Acceptances. Used by the health surface and
// `dotkeeper subscriptions list --verbose`.
type StatusEntry struct {
	Subscription config.SubscriptionEntry
	Status       SubscriptionStatus
	// Offers lists every offer that matched, even when Status is
	// Ambiguous. Empty for Pending. One entry for Accepted.
	Offers []Offer
}

// Result is the resolver's complete output. Acceptances is the
// short list reconcile acts on; Statuses is the full picture for
// the operator-facing surface.
type Result struct {
	Acceptances []Acceptance
	Statuses    []StatusEntry
}

// Resolve matches subscriptions against offers. Pure function;
// O(N+M) in offers + subscriptions because matching is by hashmap
// keyed on canonical URL (and name for non-git fallback).
//
// Matching rules:
//
//   - Canonical-URL subscription matches an offer whose Label
//     equals the subscription's Canonical.
//   - Name-based subscription matches an offer whose Label
//     contains the name pattern `dk:<Name>`. (Non-git folders
//     don't carry a canonical URL; the matcher relies on this
//     conventional label prefix written by `dotkeeper track`
//     when no remote is configured.)
//   - Local-already-has-the-folder is the caller's concern: pass
//     in a `localFolderIDs` set so Resolve can skip emitting an
//     Acceptance for offers the local already has. Prevents
//     "accept the folder we already have" race churn.
//   - Multiple peers offering the SAME folderID with the SAME
//     label → one Acceptance, picking the first peer in
//     iteration order (deterministic given sorted input).
//   - Multiple peers offering DIFFERENT folderIDs that all match
//     one subscription → StatusAmbiguous, no Acceptance emitted.
//     Operator must disambiguate.
func Resolve(subs []config.SubscriptionEntry, offers []Offer, localFolderIDs map[string]bool) Result {
	// Index offers by their identity-bearing dimension so we can
	// look them up in O(1) per subscription.
	// urlIndex: canonical-URL label → []Offer matching that label
	// nameIndex: dk:<name> → []Offer (non-git case)
	urlIndex := make(map[string][]Offer, len(offers))
	nameIndex := make(map[string][]Offer, len(offers))
	for _, o := range offers {
		if o.Label == "" {
			continue
		}
		urlIndex[o.Label] = append(urlIndex[o.Label], o)
		nameIndex[o.Label] = append(nameIndex[o.Label], o)
	}

	var acceptances []Acceptance
	statuses := make([]StatusEntry, 0, len(subs))
	for _, sub := range subs {
		entry := StatusEntry{Subscription: sub}
		var candidates []Offer
		switch {
		case sub.Canonical != "":
			candidates = urlIndex[sub.Canonical]
		case sub.Name != "":
			candidates = nameIndex["dk:"+sub.Name]
		}
		// Filter candidates the local has already accepted.
		filtered := candidates[:0]
		for _, c := range candidates {
			if !localFolderIDs[c.FolderID] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered

		entry.Offers = candidates
		switch {
		case len(candidates) == 0:
			entry.Status = StatusPending
		case isAmbiguous(candidates):
			entry.Status = StatusAmbiguous
		default:
			entry.Status = StatusAccepted
			acceptances = append(acceptances, Acceptance{
				Subscription: sub,
				Offer:        candidates[0],
			})
		}
		statuses = append(statuses, entry)
	}
	return Result{Acceptances: acceptances, Statuses: statuses}
}

// isAmbiguous reports whether the candidates split into more than
// one distinct Syncthing folder ID. Same folder ID offered by
// multiple peers is NOT ambiguous (we pick any peer, sync starts).
// Different folder IDs for the same canonical IS ambiguous (two
// publishers; operator must intervene).
func isAmbiguous(offers []Offer) bool {
	if len(offers) <= 1 {
		return false
	}
	first := offers[0].FolderID
	for _, o := range offers[1:] {
		if o.FolderID != first {
			return true
		}
	}
	return false
}

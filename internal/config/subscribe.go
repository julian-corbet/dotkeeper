// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

// MergeSubscriptions combines declarative subscriptions from
// machine.toml with imperative subscriptions from state.toml,
// deduplicating by identity (Canonical preferred, falls back to
// Name for non-git folders).
//
// Order of precedence on conflict: machine.toml wins over
// state.toml — declarative config is the source of truth when
// both are present, so an operator who has put a subscription in
// their Nix flake can't be silently overridden by an
// imperatively-added entry that uses the same identity. This
// mirrors how Peers are merged.
//
// Returns a fresh slice; callers may sort or mutate without
// affecting the source arguments.
func MergeSubscriptions(declarative, imperative []SubscriptionEntry) []SubscriptionEntry {
	out := make([]SubscriptionEntry, 0, len(declarative)+len(imperative))
	seen := make(map[string]bool, len(declarative)+len(imperative))
	add := func(s SubscriptionEntry) {
		key := subscriptionKey(s)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, s := range declarative {
		add(s)
	}
	for _, s := range imperative {
		add(s)
	}
	return out
}

// subscriptionKey returns the dedup key for a SubscriptionEntry.
// Canonical takes precedence; Name is the fallback for non-git
// subscriptions. Empty when both are blank (caller should reject
// such entries before they reach here, but defensive empty-key
// causes the dedup loop to skip rather than crash).
func subscriptionKey(s SubscriptionEntry) string {
	if s.Canonical != "" {
		return "url:" + s.Canonical
	}
	if s.Name != "" {
		return "name:" + s.Name
	}
	return ""
}

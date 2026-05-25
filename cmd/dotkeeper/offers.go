// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
	"github.com/julian-corbet/dotkeeper/internal/subscribe"
	"github.com/spf13/cobra"
)

// offersCmd surfaces what folders paired peers are currently
// advertising that this machine hasn't accepted yet. The
// equivalent of `dotkeeper subscriptions list` for the
// peer-side: "what's available", not "what have I declared".
//
// Output columns:
//
//   - PEER: the human-readable peer name that's offering it
//   - REPO: the folder's canonical-URL label (or "dk:<name>"
//     for non-git folders, or raw folder ID for non-dotkeeper
//     Syncthing instances on the mesh)
//   - STATUS: "not subscribed" / "subscribed (pending peer)" /
//     "subscribed (will accept on next reconcile)"
//   - ACTION: the shell-paste-ready `dotkeeper subscribe` line
//
// This is the discovery surface that scales the product from
// 2 to 200 machines: you can't memorise peer/folder names at
// scale, so the CLI shows you what's offered and the exact
// command to opt in.
func offersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "offers",
		Short: "List folders peers are offering that this machine could subscribe to",
		Long: "Reads Syncthing's pending-folders API + the local subscription\n" +
			"list and renders a table of (peer, folder, status, action).\n" +
			"Useful as an onboarding aid on a fresh machine: see what's\n" +
			"available, paste the exact `dotkeeper subscribe` line for\n" +
			"each folder you want.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOffers()
		},
	}
}

func runOffers() error {
	machine, err := config.LoadMachineConfigV2()
	if err != nil || machine == nil {
		return fmt.Errorf("load machine.toml: %w", err)
	}
	state, err := config.LoadStateV2()
	if err != nil {
		return fmt.Errorf("load state.toml: %w", err)
	}

	// Build the peer device-ID → name map so the offers display
	// human-readable peer names.
	peerNameByID := make(map[string]string, len(machine.Peers)+1)
	for _, p := range machine.Peers {
		peerNameByID[p.DeviceID] = p.Name
	}
	if state != nil {
		for _, p := range state.Peers {
			if _, ok := peerNameByID[p.DeviceID]; !ok {
				peerNameByID[p.DeviceID] = p.Name
			}
		}
	}

	// Reach the local Syncthing for the pending-folders feed.
	key, err := engine().APIKey()
	if err != nil {
		return fmt.Errorf("Syncthing not initialised: %w", err)
	}
	c := stclient.New(key)
	pending, err := c.GetPendingFolders()
	if err != nil {
		return fmt.Errorf("query pending folders: %w", err)
	}

	// Translate to subscribe.Offer for the matcher; deterministic
	// order so the output is stable across runs.
	offers := make([]subscribe.Offer, 0, len(pending))
	for _, p := range pending {
		name := peerNameByID[p.ReceivedFrom]
		if name == "" {
			name = p.ReceivedFrom // fall back to raw device ID
		}
		offers = append(offers, subscribe.Offer{
			FolderID:     p.ID,
			Label:        p.Label,
			FromPeerName: name,
			FromDeviceID: p.ReceivedFrom,
		})
	}
	sort.Slice(offers, func(i, j int) bool {
		if offers[i].FromPeerName != offers[j].FromPeerName {
			return offers[i].FromPeerName < offers[j].FromPeerName
		}
		return offers[i].Label < offers[j].Label
	})

	if len(offers) == 0 {
		fmt.Println("no folders being offered by peers right now")
		fmt.Println()
		fmt.Println("If you expected something here, check that:")
		fmt.Println("  - the publishing peer is reachable (`dotkeeper status` / Syncthing UI)")
		fmt.Println("  - you've paired both directions (`dotkeeper peer add`)")
		fmt.Println("  - the peer is running dotkeeper v1.2+ (older versions don't set folder labels)")
		return nil
	}

	// Build a set of already-subscribed identities so we can mark
	// each offer's status.
	declSubs := machine.Subscribe
	impSubs := state.Subscriptions
	merged := config.MergeSubscriptions(declSubs, impSubs)
	subscribedCanonicals := make(map[string]bool, len(merged))
	subscribedNames := make(map[string]bool, len(merged))
	for _, s := range merged {
		if s.Canonical != "" {
			subscribedCanonicals[s.Canonical] = true
		}
		if s.Name != "" {
			subscribedNames[s.Name] = true
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PEER\tREPO\tSTATUS\tACTION")
	for _, o := range offers {
		label := o.Label
		if label == "" {
			label = "(no label) " + o.FolderID
		}

		// Determine status:
		status := "not subscribed"
		action := "dotkeeper subscribe " + label
		if subscribedCanonicals[o.Label] {
			status = "subscribed (waiting for reconcile)"
			action = "-"
		} else if labelMatchesAnyName(o.Label, subscribedNames) {
			status = "subscribed (waiting for reconcile)"
			action = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.FromPeerName, label, status, action)
	}
	return w.Flush()
}

// labelMatchesAnyName checks whether a name-based subscription
// matches the offer's "dk:<name>" label. Mirrors the resolver's
// convention.
func labelMatchesAnyName(label string, subscribedNames map[string]bool) bool {
	const prefix = "dk:"
	if len(label) <= len(prefix) || label[:len(prefix)] != prefix {
		return false
	}
	return subscribedNames[label[len(prefix):]]
}

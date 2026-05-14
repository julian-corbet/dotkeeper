// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

func peerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peer",
		Short: "Manage imperative Syncthing peers in state.toml",
		Long: "Declarative peers may be generated into machine.toml by Home Manager.\n" +
			"These commands manage the imperative peer roster in state.toml for\n" +
			"non-Nix setups and bootstrap scripts.",
	}
	cmd.AddCommand(peerAddCmd())
	cmd.AddCommand(peerListCmd())
	cmd.AddCommand(peerRemoveCmd())
	return cmd
}

func peerAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <device-id>",
		Short: "Add or update a peer in state.toml",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, deviceID := args[0], args[1]
			now := time.Now().UTC()
			updated := false
			if err := config.MutateStateV2(func(state *config.StateV2) error {
				for i, p := range state.Peers {
					if p.Name == name || p.DeviceID == deviceID {
						state.Peers[i] = config.PeerEntry{Name: name, DeviceID: deviceID, LearnedAt: now}
						updated = true
						return nil
					}
				}
				state.Peers = append(state.Peers, config.PeerEntry{Name: name, DeviceID: deviceID, LearnedAt: now})
				return nil
			}); err != nil {
				return err
			}
			if updated {
				fmt.Printf("[dotkeeper] peer updated: %s\n", name)
			} else {
				fmt.Printf("[dotkeeper] peer added: %s\n", name)
			}
			return nil
		},
	}
}

func peerListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List peers from machine.toml and state.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			peers := mergedPeersForDisplay()
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(peers)
			}
			if len(peers) == 0 {
				fmt.Println("No peers configured.")
				return nil
			}
			for _, p := range peers {
				fmt.Printf("%s\t%s\t%s\n", p.Source, p.Name, p.DeviceID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print peers as JSON")
	return cmd
}

func peerRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name-or-device-id>",
		Short: "Remove an imperative peer from state.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := config.MutateStateV2(func(state *config.StateV2) error {
				filtered := make([]config.PeerEntry, 0, len(state.Peers))
				for _, p := range state.Peers {
					if p.Name != key && p.DeviceID != key {
						filtered = append(filtered, p)
					}
				}
				state.Peers = filtered
				return nil
			}); err != nil {
				return err
			}
			fmt.Printf("[dotkeeper] peer removed from state.toml: %s\n", key)
			return nil
		},
	}
}

type peerDisplay struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	DeviceID string `json:"device_id"`
}

func mergedPeersForDisplay() []peerDisplay {
	var out []peerDisplay
	seen := make(map[string]bool)
	if machine, err := config.LoadMachineConfigV2(); err == nil && machine != nil {
		for _, p := range machine.Peers {
			if p.DeviceID == "" || seen[p.DeviceID] {
				continue
			}
			seen[p.DeviceID] = true
			out = append(out, peerDisplay{Source: "machine", Name: p.Name, DeviceID: p.DeviceID})
		}
	}
	if state, err := config.LoadStateV2(); err == nil && state != nil {
		for _, p := range state.Peers {
			if p.DeviceID == "" || seen[p.DeviceID] {
				continue
			}
			seen[p.DeviceID] = true
			out = append(out, peerDisplay{Source: "state", Name: p.Name, DeviceID: p.DeviceID})
		}
	}
	return out
}

func loadOrCreateCLIState() (*config.StateV2, error) {
	state, err := config.LoadStateV2()
	if err != nil {
		return nil, err
	}
	if state != nil {
		if state.SchemaVersion == 0 {
			state.SchemaVersion = 2
		}
		return state, nil
	}
	return &config.StateV2{
		SchemaVersion:    2,
		Peers:            []config.PeerEntry{},
		TrackedOverrides: []string{},
		ObservedRepos:    make(map[string]config.ObservedRepo),
		LastSeenPeers:    make(map[string]time.Time),
	}, nil
}

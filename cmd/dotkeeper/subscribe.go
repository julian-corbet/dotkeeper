// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/gitident"
	"github.com/spf13/cobra"
)

// subscribeCmd writes a subscription entry into state.toml that
// tells the daemon "I want this folder synced from any peer that
// has it." The reconciler (PR γ) reads the merged subscription
// list (machine.toml + state.toml), matches against peers'
// ClusterConfig advertisements by canonical-URL label, and
// provisions the folder locally + adds it to Syncthing's config.
//
// Identity rules:
//
//   - The positional argument is interpreted as a git remote URL,
//     normalised via gitident.Canonical. The HTTPS, SCP-style,
//     and ssh:// forms all collapse to the same canonical.
//   - When --name is passed instead, the entry is a non-git
//     subscription keyed by logical name. Used for dotfiles dirs
//     or scratch areas that have no remote.
//   - --path overrides the default mirror-path placement. The
//     default is determined by the reconciler at provisioning
//     time (PR γ); the CLI just records the operator's choice.
func subscribeCmd() *cobra.Command {
	var pathFlag string
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "subscribe <git-url-or-canonical>",
		Short: "Declare that this machine wants a folder synced from any peer that has it",
		Long: "Records a subscription in state.toml. The daemon then matches\n" +
			"the subscription against peers' folder offerings (by canonical\n" +
			"git-remote URL) and provisions the folder locally on first match.\n\n" +
			"Examples:\n" +
			"  dotkeeper subscribe github.com/foo/rag\n" +
			"  dotkeeper subscribe git@github.com:foo/rag.git           # any URL form works\n" +
			"  dotkeeper subscribe --name dotfiles                       # non-git folder\n" +
			"  dotkeeper subscribe github.com/foo/rag --path=/code/rag   # custom local path\n\n" +
			"Declarative subscriptions can also live in machine.toml's\n" +
			"[[subscribe]] sections — the daemon merges both sources.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSubscribe(args, pathFlag, nameFlag)
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"Override the local path where the folder lands (default: mirror convention)")
	cmd.Flags().StringVar(&nameFlag, "name", "",
		"Subscribe by logical name (for non-git folders); mutually exclusive with positional URL")
	return cmd
}

func runSubscribe(args []string, pathFlag, nameFlag string) error {
	if nameFlag != "" && len(args) > 0 {
		return errors.New("--name and a positional URL are mutually exclusive")
	}
	if nameFlag == "" && len(args) == 0 {
		return errors.New("must pass a git URL positionally or --name=NAME for non-git folders")
	}

	entry := config.SubscriptionEntry{
		Path:    pathFlag,
		AddedAt: time.Now().UTC(),
	}
	if nameFlag != "" {
		entry.Name = nameFlag
	} else {
		canonical, err := gitident.Canonical(args[0])
		if err != nil {
			return fmt.Errorf("parse %q: %w", args[0], err)
		}
		entry.Canonical = canonical
	}

	state, err := config.LoadStateV2()
	if err != nil {
		return fmt.Errorf("load state.toml: %w", err)
	}
	if state == nil {
		return errors.New("state.toml not found — run `dotkeeper init` first")
	}
	// Reject duplicate of an existing imperative subscription. We
	// deliberately don't check declarative (machine.toml) entries
	// here — the merge logic at reconcile time deduplicates anyway,
	// and an operator running `subscribe` for something already in
	// their flake probably wants the imperative entry as an
	// override expression (it'll lose to declarative on conflict;
	// we just don't fight them).
	for _, existing := range state.Subscriptions {
		if subscriptionsMatch(existing, entry) {
			return fmt.Errorf("already subscribed: %s", subscriptionIdentity(entry))
		}
	}
	state.Subscriptions = append(state.Subscriptions, entry)
	if err := config.WriteStateV2(state); err != nil {
		return fmt.Errorf("write state.toml: %w", err)
	}
	fmt.Printf("✓ subscribed: %s\n", subscriptionIdentity(entry))
	if pathFlag != "" {
		fmt.Printf("  local path: %s\n", pathFlag)
	} else {
		fmt.Printf("  local path: (mirror convention — first scan_root + basename)\n")
	}
	fmt.Printf("  next reconcile picks it up; force with `dotkeeper reconcile`\n")
	return nil
}

func unsubscribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <git-url-or-name>",
		Short: "Remove a previously-declared subscription",
		Long: "Removes the matching entry from state.toml. Only imperative\n" +
			"subscriptions (added via `dotkeeper subscribe`) can be removed\n" +
			"this way; declarative entries in machine.toml must be removed\n" +
			"by editing that file (or regenerating from your Nix flake).",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runUnsubscribe(args[0])
		},
	}
}

func runUnsubscribe(arg string) error {
	// Try parsing as URL first; if it fails, treat as name.
	target := config.SubscriptionEntry{}
	if canonical, err := gitident.Canonical(arg); err == nil {
		target.Canonical = canonical
	} else {
		target.Name = arg
	}

	state, err := config.LoadStateV2()
	if err != nil {
		return fmt.Errorf("load state.toml: %w", err)
	}
	if state == nil {
		return errors.New("state.toml not found")
	}
	filtered := state.Subscriptions[:0]
	removed := false
	for _, existing := range state.Subscriptions {
		if subscriptionsMatch(existing, target) {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if !removed {
		return fmt.Errorf("no imperative subscription matches %q (declarative entries in machine.toml are not removable here)", arg)
	}
	state.Subscriptions = filtered
	if err := config.WriteStateV2(state); err != nil {
		return fmt.Errorf("write state.toml: %w", err)
	}
	fmt.Printf("✓ unsubscribed: %s\n", subscriptionIdentity(target))
	return nil
}

func subscriptionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List subscriptions (declarative + imperative, merged)",
		Long: "Shows the merged subscription list as the reconciler sees it:\n" +
			"declarative entries from machine.toml first, then imperative\n" +
			"entries from state.toml. Entries with identical identity are\n" +
			"deduplicated; declarative wins on conflict.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSubscriptionsList()
		},
	}
}

func runSubscriptionsList() error {
	machine, err := config.LoadMachineConfigV2()
	if err != nil {
		return fmt.Errorf("load machine.toml: %w", err)
	}
	state, err := config.LoadStateV2()
	if err != nil {
		return fmt.Errorf("load state.toml: %w", err)
	}
	var decl []config.SubscriptionEntry
	if machine != nil {
		decl = machine.Subscribe
	}
	var imp []config.SubscriptionEntry
	if state != nil {
		imp = state.Subscriptions
	}
	merged := config.MergeSubscriptions(decl, imp)
	if len(merged) == 0 {
		fmt.Println("no subscriptions declared")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "IDENTITY\tSOURCE\tPATH\tADDED")
	// Build a fast lookup so we can tag each merged entry by its
	// origin (declarative vs imperative).
	declKeys := make(map[string]bool, len(decl))
	for _, d := range decl {
		declKeys[subscriptionIdentity(d)] = true
	}
	for _, s := range merged {
		source := "imperative"
		if declKeys[subscriptionIdentity(s)] {
			source = "declarative"
		}
		path := s.Path
		if path == "" {
			path = "(mirror)"
		}
		added := "-"
		if !s.AddedAt.IsZero() {
			added = s.AddedAt.Format("2006-01-02")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			subscriptionIdentity(s), source, path, added)
	}
	return w.Flush()
}

// subscriptionsMatch reports whether two SubscriptionEntries
// reference the same folder. Comparison is by identity only —
// Path and AddedAt differences are irrelevant for dedup.
func subscriptionsMatch(a, b config.SubscriptionEntry) bool {
	if a.Canonical != "" || b.Canonical != "" {
		return a.Canonical == b.Canonical
	}
	return a.Name == b.Name
}

// subscriptionIdentity returns a human-readable identity string.
// Used in CLI output and error messages.
func subscriptionIdentity(s config.SubscriptionEntry) string {
	if s.Canonical != "" {
		return s.Canonical
	}
	if s.Name != "" {
		return "(non-git) " + s.Name
	}
	return "(invalid: no identity)"
}

// subscriptionsCmd is the parent verb that groups list under
// the existing `dotkeeper` surface. `subscribe` and `unsubscribe`
// live at the top level so they're discoverable as actions.
func subscriptionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subscriptions",
		Short: "Inspect declared folder subscriptions",
	}
	cmd.AddCommand(subscriptionsListCmd())
	return cmd
}

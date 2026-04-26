// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// dotkeeper — P2P repo sync with git history.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/conflict"
	"github.com/julian-corbet/dotkeeper/internal/doctor"
	"github.com/julian-corbet/dotkeeper/internal/service"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
	"github.com/julian-corbet/dotkeeper/internal/stengine"
)

// Version is dotkeeper's release version. It overrides Syncthing's embedded
// build.Version for BEP handshake purposes (see internal/stengine).
// Injected via -ldflags="-X main.version=..." at release build time.
var (
	version = "0.1.1"
	commit  = "none"
)

func main() {
	// Wire SIGINT/SIGTERM into the root context so cmd.Context() inside
	// every subcommand carries cancellation. Without ExecuteContext, a
	// long-running reconcile (or any blocking subcommand work) would not
	// observe Ctrl-C until it returned naturally.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	root := &cobra.Command{
		Use:   "dotkeeper",
		Short: "P2P repo sync with git history",
		Long:  "Embeds Syncthing for real-time file sync between machines.\nUses git for history and backup.",
	}

	root.AddCommand(versionCmd())
	root.AddCommand(initCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(startCmd())
	root.AddCommand(conflictCmd())
	root.AddCommand(doctorCmd())
	// v0.5 declarative commands
	root.AddCommand(reconcileCmd())
	root.AddCommand(identityCmd())
	root.AddCommand(trackCmd())
	root.AddCommand(untrackCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

// --- Helpers ---

func engine() *stengine.Engine {
	return stengine.New(config.STConfigDir(), config.STDataDir(), version)
}

func apiClient() *stclient.Client {
	eng := engine()
	key, err := eng.APIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: cannot read API key: %v\n", err)
		os.Exit(1)
	}
	return stclient.New(key)
}

func truncateID(id string) string {
	if len(id) >= 8 {
		return id[:8] + "..."
	}
	return id
}

func svcManager() service.Manager {
	mgr, err := service.Detect()
	if err != nil || mgr == nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: service detection failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "[dotkeeper] falling back to manual mode — start Syncthing with: dotkeeper start")
		// Return a no-op manager that won't panic
		return &service.NoopManager{}
	}
	return mgr
}

func binaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "dotkeeper"
	}
	return exe
}

// --- Commands ---

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print dotkeeper version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("dotkeeper %s (%s)\n", version, commit)
		},
	}
}

func initCmd() *cobra.Command {
	var name string
	var slot int
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize this machine for dotkeeper",
		Long:  "Creates machine identity (machine.toml v2), generates Syncthing keys, and installs services.\nRun this on the first machine.",
		Run: func(cmd *cobra.Command, args []string) {
			existing, _ := config.LoadMachineConfigV2()
			if existing != nil && !force {
				fmt.Printf("[dotkeeper] already initialized as '%s' (slot %d)\n", existing.Name, existing.Slot)
				eng := engine()
				if id, err := eng.DeviceID(); err == nil {
					fmt.Printf("[dotkeeper] device ID: %s\n", id)
				}
				fmt.Println("[dotkeeper] use --force to reinitialize")
				return
			}

			if name == "" {
				hostname, _ := os.Hostname()
				name = hostname
			}
			if slot < 0 {
				slot = 0
			}

			// 1. Machine identity (v2 schema)
			mcfg := &config.MachineConfigV2{
				SchemaVersion:            2,
				Name:                     name,
				Slot:                     uint(slot),
				DefaultCommitPolicy:      "manual",
				DefaultGitInterval:       "hourly",
				DefaultSlotOffsetMinutes: 5,
				ReconcileInterval:        "5m",
				DefaultShareWith:         []string{},
				Discovery: config.DiscoveryConfig{
					ScanRoots:    []string{"~/Documents/GitHub", "~/.agent"},
					Exclude:      []string{},
					ScanInterval: "5m",
					ScanDepth:    3,
				},
			}
			if err := config.WriteMachineConfigV2(mcfg); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[dotkeeper] machine: %s (slot %d)\n", name, slot)

			// 2. Syncthing identity
			eng := engine()
			if err := eng.Setup(); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			deviceID, err := eng.DeviceID()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[dotkeeper] Syncthing identity generated\n")

			// 3. Syncthing service
			mgr := svcManager()
			binPath := binaryPath()
			if err := mgr.InstallSyncthing(binPath); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: service install: %v\n", err)
			} else {
				fmt.Printf("[dotkeeper] started Syncthing service (%s)\n", service.PlatformName(mgr))
			}

			// 4. Print result
			fmt.Println()
			fmt.Printf("[dotkeeper] device ID: %s\n", deviceID)
			fmt.Println()
			fmt.Println("[dotkeeper] to add repos, drop a dotkeeper.toml in any repo under a scan root,")
			fmt.Println("[dotkeeper] or run: dotkeeper track <path>")
			fmt.Println("[dotkeeper] then run: dotkeeper reconcile")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "machine name (default: hostname)")
	cmd.Flags().IntVar(&slot, "slot", -1, "timer slot (default: 0)")
	cmd.Flags().BoolVar(&force, "force", false, "reinitialize")
	return cmd
}

// statusCmd shows the v0.5 machine status: machine name, slot, scan roots,
// peer count from state.toml, tracked override count, and Syncthing connection.
func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show full status",
		Run: func(cmd *cobra.Command, args []string) {
			machine, _ := config.LoadMachineConfigV2()
			state, _ := config.LoadStateV2()

			fmt.Println("=== Machine ===")
			if machine != nil {
				fmt.Printf("  Name: %s\n", machine.Name)
				fmt.Printf("  Slot: %d\n", machine.Slot)
				fmt.Printf("  Scan roots: %s\n", strings.Join(machine.Discovery.ScanRoots, ", "))
			} else {
				fmt.Println("  Not initialized (run 'dotkeeper init')")
			}

			fmt.Printf("\n=== Syncthing (embedded, API %s) ===\n", stclient.APIAddress)
			eng := engine()
			deviceID, _ := eng.DeviceID()

			mgr := svcManager()
			if mgr.IsSyncthingRunning() {
				fmt.Println("  Status: running")
				if deviceID != "" {
					fmt.Printf("  Device ID: %s\n", deviceID)
				}
				fmt.Printf("  Sync port: 12000\n")

				client := apiClient()
				if apiCfg, err := client.GetConfig(); err == nil {
					devices, _ := apiCfg["devices"].([]any)
					folders, _ := apiCfg["folders"].([]any)
					peerCount := 0
					for _, d := range devices {
						dm, _ := d.(map[string]any)
						did, _ := dm["deviceID"].(string)
						if did != deviceID {
							peerCount++
							dname, _ := dm["name"].(string)
							fmt.Printf("  Peer: %s (%s)\n", dname, truncateID(did))
						}
					}
					if peerCount == 0 {
						fmt.Println("  Peers: none")
					}
					fmt.Printf("  Folders: %d\n", len(folders))
				}
			} else {
				fmt.Println("  Status: not running")
				if deviceID != "" {
					fmt.Printf("  Device ID: %s\n", deviceID)
				}
			}

			if state != nil {
				if len(state.Peers) > 0 {
					fmt.Printf("\n=== Peers (%d) ===\n", len(state.Peers))
					for _, p := range state.Peers {
						fmt.Printf("  %s: %s\n", p.Name, truncateID(p.DeviceID))
					}
				} else {
					fmt.Println("\n=== Peers ===")
					fmt.Println("  none (pair machines with: dotkeeper reconcile)")
				}

				if len(state.TrackedOverrides) > 0 {
					fmt.Printf("\n=== Tracked overrides (%d) ===\n", len(state.TrackedOverrides))
					for _, p := range state.TrackedOverrides {
						fmt.Printf("  %s\n", config.ContractPath(p))
					}
				}

				if len(state.ObservedRepos) > 0 {
					fmt.Printf("\n=== Observed repos (%d) ===\n", len(state.ObservedRepos))
					for path, obs := range state.ObservedRepos {
						lastReconcile := "never"
						if !obs.LastBackupAt.IsZero() {
							lastReconcile = obs.LastBackupAt.Format("2006-01-02 15:04:05")
						}
						fmt.Printf("  %s (last backup: %s)\n", config.ContractPath(path), lastReconcile)
					}
				}
			}
		},
	}
}

func startCmd() *cobra.Command {
	var debug bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start embedded Syncthing + reconcile daemon (foreground, for systemd)",
		Long: "Starts the embedded Syncthing engine, the conflict watcher, and the\n" +
			"reconcile daemon. The reconciler runs on a periodic timer and reacts to\n" +
			"filesystem changes in scan roots, state.toml, and machine.toml.\n\n" +
			"Use --debug to raise the log level to DEBUG.",
		Run: func(cmd *cobra.Command, args []string) {
			// SIGINT/SIGTERM is wired into the root context in main(), so
			// cmd.Context() already cancels on signal. No need to redo it.
			ctx := cmd.Context()

			// Configure slog handler.
			logLevel := slog.LevelInfo
			if debug {
				logLevel = slog.LevelDebug
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: logLevel,
			}))
			slog.SetDefault(logger)

			// Spin up the conflict watcher alongside Syncthing so we
			// log sync-conflict files as soon as they appear. This is a
			// best-effort: if no config is present (first boot) or no
			// managed folders exist yet, we just skip it.
			stopWatcher := startConflictWatcher(ctx)
			defer stopWatcher()

			// Start the reconcile daemon alongside Syncthing.
			startReconcileDaemon(ctx, logger)

			eng := engine()
			if err := eng.Start(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
		},
	}
	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug-level logging")
	return cmd
}

// startConflictWatcher starts a conflict.Watcher over every managed
// folder discovered from v0.5 state. Returns a cleanup function that
// closes the watcher. Failures are logged, never fatal — Syncthing must
// keep running even if the watcher can't start.
func startConflictWatcher(ctx context.Context) func() {
	roots := managedFolderPathsV5()
	if len(roots) == 0 {
		return func() {}
	}

	w, err := conflict.New(roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: conflict watcher: %v\n", err)
		return func() {}
	}

	// Load auto-resolve setting: default true, controlled by machine config
	// in a future version. For now always enabled.
	autoResolve := true

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-w.Events():
				if !ok {
					return
				}
				handleConflictEvent(ctx, c, roots, autoResolve)
			case err, ok := <-w.Errors():
				if !ok {
					return
				}
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: conflict watcher: %v\n", err)
			}
		}
	}()

	return func() {
		_ = w.Close()
		<-done
	}
}

// handleConflictEvent runs the resolver chain on one detected conflict
// and emits exactly one log line summarising the outcome. Errors from
// individual resolvers become "kept (<reason>)" log lines — they never
// escape to abort the watcher loop.
func handleConflictEvent(ctx context.Context, c conflict.Conflict, roots []string, autoResolve bool) {
	when := c.Timestamp.Format("2006-01-02 15:04:05")

	// Phase 1 behaviour (detection-only) when the user opted out.
	if !autoResolve {
		fmt.Printf("[dotkeeper] kept: %s (from device %s at %s; auto-resolve disabled)\n",
			c.Path, c.DeviceIDShort, when)
		return
	}

	// Try dedup first — cheapest, safest, and covers the commonest
	// "same save on two machines" case without touching git at all.
	if action, err := conflict.ResolveIdentical(c); err == nil && action == conflict.ActionDeduped {
		fmt.Printf("[dotkeeper] deduped: %s (from device %s at %s)\n",
			c.Path, c.DeviceIDShort, when)
		return
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: dedup check %s: %v\n", c.Path, err)
		// Fall through — maybe the merge path still works.
	}

	// Then the 3-way merge. repoRoot is the managed-folder root the
	// conflict lives under, which must also be a git repo for the
	// merge to land; non-repo folders fail gracefully below.
	repoRoot := containingFolder(c.Path, roots)
	action, err := conflict.ResolveTextMerge(ctx, c, repoRoot)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: merge %s: %v\n", c.Path, err)
		fmt.Printf("[dotkeeper] kept: %s (from device %s at %s; merge error)\n",
			c.Path, c.DeviceIDShort, when)
	case action == conflict.ActionMerged:
		fmt.Printf("[dotkeeper] merged: %s (from device %s at %s)\n",
			c.Path, c.DeviceIDShort, when)
	default:
		fmt.Printf("[dotkeeper] kept: %s (from device %s at %s; manual resolution required)\n",
			c.Path, c.DeviceIDShort, when)
	}
}

// managedFolderPathsV5 returns absolute, existing paths for every managed
// folder discovered from v0.5 state: TrackedOverrides + repos found by
// walking scan roots for dotkeeper.toml files. The config directory itself
// is always included. Missing paths are skipped silently.
func managedFolderPathsV5() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(config.ExpandPath(p))
		if err != nil {
			return
		}
		if _, err := os.Stat(abs); err != nil {
			return
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}

	// Always include the config directory.
	add(config.ConfigDir())

	// Add TrackedOverrides from state.toml.
	if state, err := config.LoadStateV2(); err == nil && state != nil {
		for _, p := range state.TrackedOverrides {
			add(p)
		}
	}

	// Walk scan roots to find repos with dotkeeper.toml.
	if machine, err := config.LoadMachineConfigV2(); err == nil && machine != nil {
		for _, root := range machine.Discovery.ScanRoots {
			expanded := config.ExpandPath(root)
			if info, err := os.Stat(expanded); err != nil || !info.IsDir() {
				continue
			}
			walkDepth := machine.Discovery.ScanDepth
			if walkDepth <= 0 {
				walkDepth = 3
			}
			_ = walkScanRoot(expanded, 0, walkDepth, func(repoPath string) {
				add(repoPath)
			})
		}
	}

	return out
}

// walkScanRoot recursively walks root up to maxDepth levels looking for
// directories that contain a dotkeeper.toml file. When found, fn is called
// with the directory path. The walk does not descend into dirs that already
// have a dotkeeper.toml.
func walkScanRoot(root string, depth, maxDepth int, fn func(string)) error {
	if depth > maxDepth {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	hasDotkeeper := false
	for _, e := range entries {
		if !e.IsDir() && e.Name() == "dotkeeper.toml" {
			hasDotkeeper = true
			break
		}
	}
	if hasDotkeeper {
		fn(root)
		return nil // don't descend into a managed repo
	}

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		_ = walkScanRoot(filepath.Join(root, e.Name()), depth+1, maxDepth, fn)
	}
	return nil
}

func conflictCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conflict",
		Short: "Inspect and resolve Syncthing sync-conflict files",
	}
	cmd.AddCommand(conflictListCmd())
	cmd.AddCommand(conflictResolveAllCmd())
	cmd.AddCommand(conflictKeepCmd())
	cmd.AddCommand(conflictAcceptCmd())
	return cmd
}

func conflictListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sync-conflict files across all managed folders",
		Run: func(cmd *cobra.Command, args []string) {
			roots := managedFolderPathsV5()
			shortToHost := deviceShortToHostnameV5()

			var all []conflict.Conflict
			for _, root := range roots {
				found, err := conflict.Scan(root)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: scanning %s: %v\n", root, err)
					continue
				}
				all = append(all, found...)
			}

			if len(all) == 0 {
				fmt.Printf("No sync conflicts detected across %d managed folders.\n", len(roots))
				return
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "FOLDER\tORIGINAL FILE\tTIMESTAMP\tFROM")
			for _, c := range all {
				folder := containingFolder(c.Path, roots)
				rel, err := filepath.Rel(folder, filepath.Dir(c.Path))
				if err != nil || rel == "." {
					rel = ""
				}
				original := c.OriginalName
				if rel != "" {
					original = filepath.Join(rel, c.OriginalName)
				}
				from := c.DeviceIDShort
				if host, ok := shortToHost[c.DeviceIDShort]; ok {
					from = host
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					config.ContractPath(folder),
					original,
					c.Timestamp.Format("2006-01-02 15:04:05"),
					from,
				)
			}
			_ = tw.Flush()
		},
	}
}

// deviceShortToHostnameV5 builds a lookup from the 7-character short-form
// device ID to the friendly machine name registered in state.toml's Peers.
func deviceShortToHostnameV5() map[string]string {
	out := make(map[string]string)
	state, err := config.LoadStateV2()
	if err != nil || state == nil {
		return out
	}
	for _, p := range state.Peers {
		if len(p.DeviceID) >= 7 {
			out[p.DeviceID[:7]] = p.Name
		}
	}
	return out
}

// conflictResolveAllCmd walks every managed folder, finds conflicts,
// and tries both resolvers in order. Useful as a one-off after an
// extended outage when many conflicts accumulate before the watcher
// goroutine sees them live.
func conflictResolveAllCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve-all",
		Short: "Scan all managed folders and auto-resolve what can be resolved",
		Long: "Walks every managed folder, runs the hash-identical dedup resolver\n" +
			"and the git-merge-file 3-way text resolver on each sync-conflict\n" +
			"file, and prints a per-file summary. Anything left as 'kept' needs\n" +
			"human judgement — diff the pair and merge manually.",
		Run: func(cmd *cobra.Command, args []string) {
			roots := managedFolderPathsV5()

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			var all []conflict.Conflict
			for _, root := range roots {
				found, err := conflict.Scan(root)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: scanning %s: %v\n", root, err)
					continue
				}
				all = append(all, found...)
			}

			if len(all) == 0 {
				fmt.Printf("No sync conflicts detected across %d managed folders.\n", len(roots))
				return
			}

			var deduped, merged, kept int
			for _, c := range all {
				// Dedup first — cheapest.
				action, err := conflict.ResolveIdentical(c)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: dedup %s: %v\n", c.Path, err)
				}
				if action == conflict.ActionDeduped {
					fmt.Printf("deduped: %s\n", c.Path)
					deduped++
					continue
				}

				// Then 3-way merge.
				repoRoot := containingFolder(c.Path, roots)
				action, err = conflict.ResolveTextMerge(ctx, c, repoRoot)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: merge %s: %v\n", c.Path, err)
				}
				switch action {
				case conflict.ActionMerged:
					fmt.Printf("merged:  %s\n", c.Path)
					merged++
				default:
					fmt.Printf("kept:    %s\n", c.Path)
					kept++
				}
			}

			fmt.Printf("\nSummary: %d deduped, %d merged, %d kept (manual resolution).\n",
				deduped, merged, kept)
		},
	}
}

// containingFolder returns the watched root that a conflict path lives
// under. Used purely for table display — the conflict.Path is already
// absolute, this just shortens the leading segment.
func containingFolder(path string, roots []string) string {
	best := ""
	for _, r := range roots {
		if strings.HasPrefix(path, r+string(filepath.Separator)) || path == r {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	if best == "" {
		return filepath.Dir(path)
	}
	return best
}

// conflictKeepCmd implements `dotkeeper conflict keep <path>`: drop the
// sync-conflict variant and leave the canonical file untouched. No git
// activity because nothing tracked changed.
func conflictKeepCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "keep <path>",
		Short: "Delete the sync-conflict variant and keep the current file as-is",
		Long: "Removes the .sync-conflict-* file for the given path and leaves the canonical\n" +
			"file unchanged. No git commit — nothing tracked changed.\n\n" +
			"<path> may be either the canonical file or the explicit variant filename.\n" +
			"With --all, processes every pending conflict in every managed folder.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all takes no arguments")
			}
			if !all && len(args) != 1 {
				return fmt.Errorf("exactly one <path> required (or use --all)")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if all {
				n := runKeepAll()
				fmt.Printf("kept %d conflict(s)\n", n)
				return
			}
			if err := runKeepOne(args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "apply to every pending conflict across all managed folders")
	return cmd
}

// conflictAcceptCmd implements `dotkeeper conflict accept <path>`:
// replace the canonical file with the variant's contents, delete the
// variant, and commit with a scoped message.
func conflictAcceptCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "accept <path>",
		Short: "Replace the current file with the sync-conflict variant and commit",
		Long: "Overwrites the canonical file with the contents of the .sync-conflict-* variant,\n" +
			"removes the variant, and creates a git commit scoped to that single path:\n" +
			"  auto: accept sync conflict for <relpath> (from <deviceShort>)\n\n" +
			"<path> may be either the canonical file or the explicit variant filename.\n" +
			"With --all, processes every pending conflict in every managed folder;\n" +
			"canonicals with multiple variants are skipped (resolve them explicitly).",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all takes no arguments")
			}
			if !all && len(args) != 1 {
				return fmt.Errorf("exactly one <path> required (or use --all)")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			if all {
				n := runAcceptAll(ctx)
				fmt.Printf("accepted %d conflict(s)\n", n)
				return
			}
			if err := runAcceptOne(ctx, args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "apply to every pending conflict across all managed folders")
	return cmd
}

// resolveTarget resolves a user-supplied <path> into the exact set of
// variants to act on. Accepts either a canonical path or a variant
// path and always returns absolute paths.
//
// Returns (variants, canonical, nil) on success. Returns a non-nil
// error when:
//   - the path corresponds to no conflict at all ("no conflict for X"),
//   - the path is a canonical with multiple variants (caller must pick
//     one explicitly); the error lists them so the user can copy one.
func resolveTarget(rawPath string) ([]conflict.Conflict, string, error) {
	absPath, err := filepath.Abs(config.ExpandPath(rawPath))
	if err != nil {
		return nil, "", fmt.Errorf("resolving %q: %w", rawPath, err)
	}

	// If the user passed a variant path directly, honour it exactly —
	// we still want to check the same directory for OTHER variants so
	// an "accept this one" on a three-way split doesn't silently leave
	// the other variant behind — but we return only the one they asked
	// for. The --all path handles cross-canonical iteration elsewhere.
	if conflict.IsConflictName(absPath) {
		parsed, err := conflict.Parse(filepath.Base(absPath))
		if err != nil {
			return nil, "", fmt.Errorf("parsing %q: %w", absPath, err)
		}
		if _, err := os.Stat(absPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("no conflict for %s", rawPath)
			}
			return nil, "", fmt.Errorf("stat %s: %w", absPath, err)
		}
		parsed.Path = absPath
		canonical := filepath.Join(filepath.Dir(absPath), parsed.OriginalName)
		return []conflict.Conflict{*parsed}, canonical, nil
	}

	// Canonical path: find every variant next to it.
	variants, err := conflict.FindVariants(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("no conflict for %s", rawPath)
		}
		return nil, "", fmt.Errorf("scanning %s: %w", filepath.Dir(absPath), err)
	}
	if len(variants) == 0 {
		return nil, "", fmt.Errorf("no conflict for %s", rawPath)
	}
	if len(variants) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s has %d variants — pass one explicitly:", rawPath, len(variants))
		for _, v := range variants {
			fmt.Fprintf(&b, "\n  %s", v.Path)
		}
		return nil, "", fmt.Errorf("%s", b.String())
	}
	return variants, absPath, nil
}

// runKeepOne implements `dotkeeper conflict keep <path>` for a single
// path argument.
func runKeepOne(rawPath string) error {
	variants, _, err := resolveTarget(rawPath)
	if err != nil {
		return err
	}
	for _, v := range variants {
		if err := conflict.Keep(v); err != nil {
			return fmt.Errorf("keep %s: %w", v.Path, err)
		}
		fmt.Printf("kept: %s\n", v.Path)
	}
	return nil
}

// runKeepAll iterates every conflict across every managed folder and
// calls Keep on each. Errors are logged but do not abort the sweep —
// one stubborn variant shouldn't prevent cleanup of the rest.
func runKeepAll() int {
	roots := managedFolderPathsV5()
	var kept int
	for _, root := range roots {
		found, err := conflict.Scan(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: scanning %s: %v\n", root, err)
			continue
		}
		for _, c := range found {
			if err := conflict.Keep(c); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: keep %s: %v\n", c.Path, err)
				continue
			}
			fmt.Printf("kept: %s\n", c.Path)
			kept++
		}
	}
	return kept
}

// runAcceptOne implements `dotkeeper conflict accept <path>` for a
// single path argument.
func runAcceptOne(ctx context.Context, rawPath string) error {
	variants, canonical, err := resolveTarget(rawPath)
	if err != nil {
		return err
	}
	roots := managedFolderPathsV5()
	repoRoot := containingFolder(canonical, roots)
	for _, v := range variants {
		if err := conflict.Accept(ctx, v, repoRoot); err != nil {
			return fmt.Errorf("accept %s: %w", v.Path, err)
		}
		fmt.Printf("accepted: %s\n", v.Path)
	}
	return nil
}

// runAcceptAll iterates every conflict across every managed folder.
// Canonicals with multiple variants are skipped with a warning — the
// user must resolve them explicitly so they pick the one they want.
func runAcceptAll(ctx context.Context) int {
	roots := managedFolderPathsV5()
	var accepted int

	// Group conflicts by canonical path first so we can detect and
	// skip multi-variant cases before any disk writes happen.
	type key struct{ canonical string }
	groups := make(map[key][]conflict.Conflict)
	for _, root := range roots {
		found, err := conflict.Scan(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: scanning %s: %v\n", root, err)
			continue
		}
		for _, c := range found {
			canonical := filepath.Join(filepath.Dir(c.Path), c.OriginalName)
			k := key{canonical: canonical}
			groups[k] = append(groups[k], c)
		}
	}

	for k, variants := range groups {
		if len(variants) > 1 {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: %s has %d variants — skipping; resolve explicitly\n",
				k.canonical, len(variants))
			continue
		}
		v := variants[0]
		repoRoot := containingFolder(k.canonical, roots)
		if err := conflict.Accept(ctx, v, repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: accept %s: %v\n", v.Path, err)
			continue
		}
		fmt.Printf("accepted: %s\n", v.Path)
		accepted++
	}
	return accepted
}

// doctorCmd wires `dotkeeper doctor` to the internal/doctor orchestrator.
// Supports --json for machine-readable output. Exit codes:
//
//	0 — no failures (warnings don't count)
//	1 — at least one failed check
//	2 — catastrophic (can't construct the check set at all)
func doctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run self-diagnostic checks and report health",
		Long: "Runs a sequence of small checks (version, config, service, Syncthing API,\n" +
			"peers, folders, git remotes, backup timer, sync conflicts) and prints a\n" +
			"one-line verdict per check. Output is useful to paste into an issue report.\n\n" +
			"Pass --json for machine-readable output.",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			checks := buildDoctorChecks()
			var fails int
			if asJSON {
				fails = doctor.RunJSON(ctx, checks, os.Stdout)
			} else {
				fails = doctor.Run(ctx, checks, os.Stdout)
			}
			if fails > 0 {
				os.Exit(1)
			}
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON output")
	return cmd
}

// buildDoctorChecks constructs the ordered list of checks the doctor
// subcommand runs. Dependencies (service manager, Syncthing client) are
// resolved best-effort — if any can't be built, the relevant checks
// will still report cleanly (Warn/Fail with a hint) rather than aborting.
func buildDoctorChecks() []doctor.Check {
	mgr, _ := service.Detect()
	// Build an stclient best-effort: the API key is only readable once
	// Syncthing has been initialised. On a fresh box we simply pass a
	// nil client and let SyncthingAPICheck report a clear Fail.
	var client doctor.STClient
	if key, err := engine().APIKey(); err == nil {
		client = stclient.New(key)
	}
	return []doctor.Check{
		doctor.VersionCheck{Version: version, Commit: commit},
		doctor.ConfigCheck{},
		doctor.ServiceCheck{Manager: mgr},
		doctor.SyncthingAPICheck{Client: client},
		doctor.PeersCheck{Client: client},
		doctor.FoldersCheck{Client: client},
		doctor.GitRemotesCheck{},
		doctor.ConflictsCheck{},
	}
}

// unused keeps the time import live for the statusCmd's time.Time formatting.
var _ = time.Time{}

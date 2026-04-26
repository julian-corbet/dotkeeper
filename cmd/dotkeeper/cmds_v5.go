// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// cmds_v5.go wires the v0.5 declarative subcommands into the CLI:
//   - reconcile  — single synchronous reconcile pass
//   - identity   — print machine name + Syncthing device ID
//   - track      — register a git repo outside any scan root in state.toml
//   - untrack    — remove from state.toml

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/reconcile"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// reconcileCmd implements 'dotkeeper reconcile [<path>]'.
// It runs a single synchronous reconcile pass, prints the plan + outcomes,
// exits 0 on success or 1 if any action failed.
func reconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile [<path>]",
		Short: "Run a single reconcile pass and print the plan",
		Long: "Computes the difference between desired and observed state, prints\n" +
			"each planned action, and applies them. Optional <path> argument\n" +
			"scopes discovery to a single repo directory.\n\n" +
			"Exits 0 on success, 1 if any action failed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			machinePath := config.MachineConfigPath()
			statePath := config.StateV2Path()

			// Fail early with a helpful message when not yet initialised.
			if _, err := os.Stat(machinePath); os.IsNotExist(err) {
				return fmt.Errorf("not initialized — run 'dotkeeper init' first")
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))

			desired := reconcile.NewDesiredProvider(machinePath, statePath)
			observed := buildObservedProvider(statePath)

			applier := &reconcile.RealApplier{
				ST:     nil, // populated below if Syncthing is reachable
				Logger: logger,
			}

			// Wire up the Syncthing client if the API key is available.
			if key, err := engine().APIKey(); err == nil {
				applier.ST = stclient.New(key)
			}

			r := &reconcile.Reconciler{
				Desired:  desired,
				Observed: observed,
				Applier:  applier,
				Logger:   logger,
			}

			ctx := cmd.Context()
			plan, err := r.Reconcile(ctx)

			if len(plan) == 0 {
				fmt.Println("[dotkeeper] reconcile: no actions needed")
			} else {
				fmt.Printf("[dotkeeper] reconcile: %d action(s)\n", len(plan))
				for _, a := range plan {
					fmt.Printf("  %s\n", a.Describe())
				}
			}

			if err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] reconcile: apply error: %v\n", err)
				return fmt.Errorf("reconcile apply failed: %w", err)
			}
			return nil
		},
	}
}

// identityCmd implements 'dotkeeper identity'.
// Prints this machine's name and Syncthing device ID.
// With --device-id, prints only the device ID (for shell scripting).
func identityCmd() *cobra.Command {
	var deviceIDOnly bool

	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Print this machine's name and Syncthing device ID",
		Long: "Reads machine.toml for the machine name and state.toml for the\n" +
			"Syncthing device ID. Output format:\n\n" +
			"  name: <machine-name>\n" +
			"  device_id: <XXXXXXX-...>\n\n" +
			"Pass --device-id to print just the device ID (for shell scripting).",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load machine config (v2 first, fall back to v1).
			machineName, err := loadMachineName()
			if err != nil {
				return err
			}

			// Get the device ID from the Syncthing engine.
			eng := engine()
			deviceID, err := eng.DeviceID()
			if err != nil || deviceID == "" {
				return fmt.Errorf("Syncthing device ID not available — run 'dotkeeper init' first")
			}

			if deviceIDOnly {
				fmt.Println(deviceID)
				return nil
			}

			fmt.Printf("name: %s\n", machineName)
			fmt.Printf("device_id: %s\n", deviceID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&deviceIDOnly, "device-id", false, "print only the device ID")
	return cmd
}

// trackCmd implements 'dotkeeper track <path>'.
// Registers an absolute path to a git repo in state.toml's tracked_overrides.
func trackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track <path>",
		Short: "Register a git repo outside any scan root in state.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPath := args[0]
			absPath, err := filepath.Abs(config.ExpandPath(rawPath))
			if err != nil {
				return fmt.Errorf("resolving path %q: %w", rawPath, err)
			}

			// Validate it is a git repo.
			if _, err := os.Stat(filepath.Join(absPath, ".git")); err != nil {
				return fmt.Errorf("%s is not a git repository (no .git directory)", absPath)
			}

			// Apply the TrackRepo action directly via RealApplier.
			applier := &reconcile.RealApplier{Logger: slog.Default()}
			if err := applier.Apply(cmd.Context(), reconcile.TrackRepo{Path: absPath}); err != nil {
				return fmt.Errorf("track %s: %w", absPath, err)
			}

			fmt.Printf("[dotkeeper] tracked: %s\n", absPath)
			return nil
		},
	}
}

// untrackCmd implements 'dotkeeper untrack <path>'.
// Removes a path from state.toml's tracked_overrides. Idempotent.
func untrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrack <path>",
		Short: "Remove a repo from state.toml tracked overrides",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPath := args[0]
			absPath, err := filepath.Abs(config.ExpandPath(rawPath))
			if err != nil {
				return fmt.Errorf("resolving path %q: %w", rawPath, err)
			}

			// Apply the UntrackRepo action — idempotent no-op if not present.
			applier := &reconcile.RealApplier{Logger: slog.Default()}
			if err := applier.Apply(cmd.Context(), reconcile.UntrackRepo{Path: absPath}); err != nil {
				return fmt.Errorf("untrack %s: %w", absPath, err)
			}

			fmt.Printf("[dotkeeper] untracked: %s\n", absPath)
			return nil
		},
	}
}

// --- start rewrite helpers ---

// buildObservedProvider returns an ObservedProvider wired to the live Syncthing
// instance (if available) and state.toml. If the Syncthing API key is
// unavailable the provider falls back to a nil client (no folder/peer query).
//
// Passing a typed-nil *stclient.Client to NewObservedProvider would produce a
// non-nil interface value and trigger a nil-pointer dereference inside
// querySyncthing, so we only pass a non-nil client when one is actually available.
func buildObservedProvider(statePath string) reconcile.ObservedProvider {
	if key, err := engine().APIKey(); err == nil {
		return reconcile.NewObservedProvider(stclient.New(key), statePath)
	}
	return reconcile.NewObservedProvider(nil, statePath)
}

// loadMachineName returns the machine name from machine.toml (v2 schema) or
// the legacy v1 machine.toml. Returns an error if neither is initialised.
func loadMachineName() (string, error) {
	// Try v2 schema first.
	if m, err := config.LoadMachineConfigV2(); err == nil && m != nil {
		return m.Name, nil
	}
	// Fall back to v1.
	if m, err := config.LoadMachineConfig(); err == nil && m != nil {
		return m.Name, nil
	}
	return "", fmt.Errorf("not initialized — run 'dotkeeper init' first")
}

// startReconcileDaemon constructs a Reconciler from real providers and starts
// the reconcile loop (timer + fsnotify). It is called from startCmd.
// Errors during construction are logged but never fatal — Syncthing must keep
// running even if the reconcile daemon can't start.
func startReconcileDaemon(ctx context.Context, logger *slog.Logger) {
	machinePath := config.MachineConfigPath()
	statePath := config.StateV2Path()

	if _, err := os.Stat(machinePath); os.IsNotExist(err) {
		logger.WarnContext(ctx, "machine.toml not found — reconcile daemon disabled until 'dotkeeper init' is run")
		return
	}

	// Load machine config to get ReconcileInterval and ScanRoots for watching.
	machine, err := config.LoadMachineConfigV2()
	if err != nil || machine == nil {
		logger.WarnContext(ctx, "could not load machine config", "err", err)
		return
	}

	reconcileInterval, err := time.ParseDuration(machine.ReconcileInterval)
	if err != nil || reconcileInterval <= 0 {
		reconcileInterval = 5 * time.Minute
	}

	desired := reconcile.NewDesiredProvider(machinePath, statePath)
	observed := buildObservedProvider(statePath)

	// Wire up Syncthing client for the applier.
	var stClient *stclient.Client
	if key, err := engine().APIKey(); err == nil {
		stClient = stclient.New(key)
	}

	applier := &reconcile.RealApplier{
		ST:     stClient,
		Logger: logger,
	}

	r := &reconcile.Reconciler{
		Desired:  desired,
		Observed: observed,
		Applier:  applier,
		Logger:   logger,
	}

	// Build the set of paths to watch with fsnotify:
	// each scan root + state.toml + machine.toml.
	var watchPaths []string
	for _, root := range machine.Discovery.ScanRoots {
		expanded := config.ExpandPath(root)
		if _, err := os.Stat(expanded); err == nil {
			watchPaths = append(watchPaths, expanded)
		}
	}
	watchPaths = append(watchPaths, statePath)
	watchPaths = append(watchPaths, machinePath)

	logger.InfoContext(ctx, "reconcile daemon starting",
		"interval", reconcileInterval,
		"watch_paths", len(watchPaths),
	)

	startReconcileLoop(ctx, r, reconcileInterval, watchPaths, logger)
}

// startReconcileLoop starts two reconcile triggers in the given context:
//  1. A time.Ticker at reconcileInterval (safety-net periodic pass).
//  2. fsnotify watches on every scan root directory, state.toml, and
//     machine.toml. On any relevant fs event, reconcile fires after a
//     1-second debounce.
//
// The loop logs errors but never aborts — individual reconcile failures must
// not bring down the daemon.
func startReconcileLoop(ctx context.Context, r *reconcile.Reconciler, interval time.Duration, watchPaths []string, logger *slog.Logger) {
	ticker := time.NewTicker(interval)

	// fsnotify watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.WarnContext(ctx, "could not start fsnotify watcher", "err", err)
	} else {
		for _, p := range watchPaths {
			if err := watcher.Add(p); err != nil {
				logger.WarnContext(ctx, "fsnotify watch failed", "path", p, "err", err)
			}
		}
	}

	go func() {
		defer ticker.Stop()
		if watcher != nil {
			defer func() { _ = watcher.Close() }()
		}

		// debounceTimer fires the next reconcile after 1s of silence.
		var debounce *time.Timer

		runReconcile := func(trigger string) {
			logger.InfoContext(ctx, "reconcile triggered", "by", trigger)
			if _, err := r.Reconcile(ctx); err != nil {
				logger.ErrorContext(ctx, "reconcile failed", "err", err)
			}
		}

		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				runReconcile("timer")

			case event, ok := <-func() <-chan fsnotify.Event {
				if watcher != nil {
					return watcher.Events
				}
				return nil
			}():
				if !ok {
					return
				}
				// Only care about dotkeeper.toml create/write/remove events.
				if filepath.Base(event.Name) != "dotkeeper.toml" &&
					filepath.Base(event.Name) != "machine.toml" &&
					filepath.Base(event.Name) != "state.toml" {
					continue
				}
				// Debounce: reset timer to 1s from last event.
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(time.Second, func() {
					runReconcile("fsnotify:" + event.Name)
				})

			case err, ok := <-func() <-chan error {
				if watcher != nil {
					return watcher.Errors
				}
				return nil
			}():
				if !ok {
					return
				}
				logger.WarnContext(ctx, "fsnotify error", "err", err)
			}
		}
	}()
}

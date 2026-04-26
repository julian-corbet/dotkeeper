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

// reconcilerIface is the narrow surface startReconcileLoop needs from a
// Reconciler. Defining it here lets unit tests inject a stub without spinning
// up a real Reconciler + its providers + Syncthing.
type reconcilerIface interface {
	Reconcile(ctx context.Context) (reconcile.Plan, error)
}

// reconcileCmd implements 'dotkeeper reconcile'.
// It runs a single synchronous reconcile pass, prints the plan + outcomes,
// exits 0 on success or 1 if any action failed.
//
// Note: a positional <path> argument was specced for scoping reconcile to a
// single repo. Implementing scoped reconcile cleanly requires a path filter
// inside DesiredProvider; that's a follow-up. Until then this command takes
// no arguments — silently accepting one would be a worse UX than rejecting it.
func reconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Run a single reconcile pass and print the plan",
		Long: "Computes the difference between desired and observed state, prints\n" +
			"each planned action, and applies them.\n\n" +
			"Exits 0 on success, 1 if any action failed.",
		Args: cobra.NoArgs,
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
				return fmt.Errorf("no Syncthing device ID — run 'dotkeeper init' first")
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
// (NewObservedProvider also has a defensive nil-check; this is belt + braces.)
func buildObservedProvider(statePath string) reconcile.ObservedProvider {
	if key, err := engine().APIKey(); err == nil {
		return reconcile.NewObservedProvider(stclient.New(key), statePath)
	}
	return reconcile.NewObservedProvider(nil, statePath)
}

// loadMachineName returns the machine name from machine.toml (v2 schema).
// Returns an error if not initialised.
func loadMachineName() (string, error) {
	if m, err := config.LoadMachineConfigV2(); err == nil && m != nil {
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

	// Build the set of roots to watch with fsnotify. The loop walks each
	// scan root recursively and adds every subdirectory; new directories
	// created at runtime get added on the fly. state.toml and machine.toml
	// are individual file watches added alongside the scan roots.
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

// startReconcileLoop wires reconcile triggers and a single serialised worker.
// Triggers:
//  1. An initial reconcile fired once on entry (so a fresh daemon doesn't
//     wait the full ReconcileInterval before doing anything).
//  2. A time.Ticker at reconcileInterval (safety net).
//  3. fsnotify watches over every directory under each scan root, plus
//     state.toml and machine.toml. New directories created under a scan
//     root are added to the watch on the fly. Events on dotkeeper.toml,
//     state.toml, or machine.toml fire reconcile after a 1-second debounce.
//
// All triggers funnel into a single buffered channel of size 1. A single
// worker goroutine drains it. Triggers that arrive while reconcile is
// already running or already pending are dropped (not queued) — at most
// one extra reconcile is ever pending, which is exactly what we want.
//
// The function returns once the watcher is initialised; the goroutines run
// until ctx is cancelled. Failures are logged but never abort the loop.
func startReconcileLoop(
	ctx context.Context,
	r reconcilerIface,
	interval time.Duration,
	watchPaths []string,
	logger *slog.Logger,
) {
	// Single-slot pending channel: triggers send "request reconcile" without
	// blocking; the worker drains them one at a time. Dropped sends are fine
	// — if a reconcile is already pending, queuing a second one would do the
	// same work twice.
	pending := make(chan string, 1)

	// requestReconcile is called by every trigger source. Non-blocking send
	// with a default branch: if the channel is full, drop and warn-log.
	requestReconcile := func(reason string) {
		select {
		case pending <- reason:
		default:
			logger.DebugContext(ctx, "reconcile already pending, dropping trigger", "reason", reason)
		}
	}

	// --- Worker goroutine: serialises every reconcile call. ---
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-pending:
				logger.InfoContext(ctx, "reconcile triggered", "by", reason)
				if _, err := r.Reconcile(ctx); err != nil {
					logger.ErrorContext(ctx, "reconcile failed", "err", err)
				}
			}
		}
	}()

	// --- Initial reconcile: fire once before the loop so the daemon is
	// useful from the moment it starts, not 5 minutes later. ---
	requestReconcile("initial")

	// --- fsnotify watcher. ---
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.WarnContext(ctx, "could not start fsnotify watcher", "err", err)
	} else {
		// For each watch path, decide whether it's a directory (scan root —
		// walk recursively) or a file (state.toml / machine.toml — add as-is).
		for _, p := range watchPaths {
			info, err := os.Stat(p)
			if err != nil {
				// File doesn't exist yet (e.g. state.toml on first boot).
				// Watch the parent dir so we see the eventual CREATE.
				parent := filepath.Dir(p)
				if _, perr := os.Stat(parent); perr == nil {
					if werr := watcher.Add(parent); werr != nil {
						logger.WarnContext(ctx, "fsnotify watch failed",
							"path", parent, "err", werr)
					}
				}
				continue
			}
			if info.IsDir() {
				addWatchRecursive(watcher, p, logger)
			} else {
				if werr := watcher.Add(p); werr != nil {
					logger.WarnContext(ctx, "fsnotify watch failed",
						"path", p, "err", werr)
				}
			}
		}
	}

	// --- Trigger goroutine: drives ticker + fsnotify into requestReconcile. ---
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if watcher != nil {
			defer func() { _ = watcher.Close() }()
		}

		// Hoist the watcher channels — read once, with nil if no watcher.
		// A receive from a nil channel blocks forever, which makes those
		// select cases inert (cleaner than the closure-returning-channel
		// trick).
		var events <-chan fsnotify.Event
		var errs <-chan error
		if watcher != nil {
			events = watcher.Events
			errs = watcher.Errors
		}

		// debounce coalesces bursts of fs events (e.g. an editor rewriting
		// a file) into a single reconcile request 1s after the last event.
		var debounce *time.Timer
		// Defensive: stop the timer on shutdown so the AfterFunc doesn't
		// fire after ctx cancellation.
		defer func() {
			if debounce != nil {
				debounce.Stop()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				requestReconcile("timer")

			case event, ok := <-events:
				if !ok {
					return
				}
				handleFsnotifyEvent(ctx, event, watcher, logger, &debounce, requestReconcile)

			case err, ok := <-errs:
				if !ok {
					return
				}
				logger.WarnContext(ctx, "fsnotify error", "err", err)
			}
		}
	}()
}

// handleFsnotifyEvent processes one fsnotify event. It auto-watches new
// directories created under any watched root and debounces dotkeeper.toml /
// state.toml / machine.toml changes into a single reconcile request.
func handleFsnotifyEvent(
	ctx context.Context,
	event fsnotify.Event,
	watcher *fsnotify.Watcher,
	logger *slog.Logger,
	debounce **time.Timer,
	requestReconcile func(string),
) {
	// New directory created under a watched root → walk and watch it.
	// This makes the daemon react when a user does `mkdir <scan-root>/new-repo`
	// then drops a dotkeeper.toml inside it.
	if event.Op&fsnotify.Create == fsnotify.Create && watcher != nil {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			addWatchRecursive(watcher, event.Name, logger)
			// Don't return — a new directory by itself doesn't warrant a
			// reconcile (no dotkeeper.toml yet), but we also don't want the
			// basename filter below to skip it; just fall through.
		}
	}

	// We only reconcile in response to dotkeeper.toml / state.toml / machine.toml
	// changes. Directory events and unrelated files are ignored.
	base := filepath.Base(event.Name)
	if base != "dotkeeper.toml" && base != "machine.toml" && base != "state.toml" {
		return
	}

	if *debounce != nil {
		(*debounce).Stop()
	}
	*debounce = time.AfterFunc(time.Second, func() {
		requestReconcile("fsnotify:" + event.Name)
	})
}

// addWatchRecursive walks root and adds every subdirectory to watcher.
// Symlinks are not followed (filepath.Walk does not follow symlinks for
// directories, which is what we want — symlinked dirs would risk infinite
// recursion across user home + scan_roots).
//
// Errors from Add are logged but not returned: a single un-watchable
// subdirectory shouldn't disable the whole tree.
func addWatchRecursive(watcher *fsnotify.Watcher, root string, logger *slog.Logger) {
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			// e.g. permission denied — log and skip this subtree.
			logger.DebugContext(context.Background(), "fsnotify walk error",
				"path", path, "err", walkErr)
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if werr := watcher.Add(path); werr != nil {
			logger.DebugContext(context.Background(), "fsnotify add failed",
				"path", path, "err", werr)
		}
		return nil
	})
	if err != nil {
		logger.WarnContext(context.Background(), "fsnotify recursive watch incomplete",
			"root", root, "err", err)
	}
}

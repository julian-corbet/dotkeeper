// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// cmds_v5.go wires the declarative subcommands into the CLI:
//   - reconcile  — single synchronous reconcile pass
//   - identity   — print machine name + Syncthing device ID
//   - track      — register a git repo outside any scan root in state.toml
//   - untrack    — remove from state.toml

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/activity"
	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/gitident"
	"github.com/julian-corbet/dotkeeper/internal/reconcile"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
	"github.com/julian-corbet/dotkeeper/internal/watchhealth"
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
			// One-shot `dotkeeper reconcile` doesn't run an activity
			// tracker. Auto-pause decisions would be wrong anyway —
			// no history means every folder looks "idle since
			// startup", which the diff would over-pause on. Pass nil
			// so LastActivityByPath stays nil and Diff skips the
			// auto-pause checks entirely.
			observed := buildObservedProvider(statePath, nil)

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
// Bootstraps .dotkeeper.toml when needed, enforces local ignore files, and
// registers an absolute path to a git repo in state.toml's tracked_overrides.
func trackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track <path>",
		Short: "Bootstrap and register a git repo outside any scan root",
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

			cfg, err := ensureRepoConfig(absPath)
			if err != nil {
				return err
			}

			applier := &reconcile.RealApplier{Logger: slog.Default()}
			if err := applier.Apply(cmd.Context(), reconcile.EnsureIgnoreFile{
				RepoPath: absPath,
				Patterns: cfg.Sync.Ignore,
			}); err != nil {
				return fmt.Errorf("prepare local ignores for %s: %w", absPath, err)
			}
			if err := applier.Apply(cmd.Context(), reconcile.TrackRepo{Path: absPath}); err != nil {
				return fmt.Errorf("track %s: %w", absPath, err)
			}

			fmt.Printf("[dotkeeper] tracked: %s\n", absPath)
			return nil
		},
	}
}

func ensureRepoConfig(repoPath string) (*config.RepoConfigV2, error) {
	cfg, err := config.LoadRepoConfigV2(repoPath)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.RepoConfigV2{
			SchemaVersion: 2,
			Meta: config.RepoMeta{
				Name:    filepath.Base(repoPath),
				Added:   time.Now().UTC().Format(time.RFC3339),
				AddedBy: "unknown",
			},
			Sync: config.RepoSyncConfig{
				SyncthingFolderID: folderIDForRepo(repoPath),
				Ignore:            []string{},
				ShareWith:         []string{},
			},
			Commit: config.RepoCommitConfig{},
			GitBackup: config.RepoGitBackupConfig{
				SkipSlots: []uint{},
			},
		}
		if name, err := loadMachineName(); err == nil {
			cfg.Meta.AddedBy = name
		}
		// Discover the git remote at track time. Best-effort: a
		// non-git folder (dotfiles dir, scratch) is legitimate and
		// produces empty Git fields, which the subscription
		// matcher treats as "name-based identity only" (legacy
		// behaviour). A git folder with a malformed remote
		// surfaces as Remote set + Canonical empty — operator
		// can fix the URL and re-run `dotkeeper track` to
		// repopulate.
		if remote := gitRemoteOrigin(repoPath); remote != "" {
			cfg.Git.Remote = remote
			if c, err := gitident.Canonical(remote); err == nil {
				cfg.Git.Canonical = c
			}
		}
		if err := config.WriteRepoConfigV2(repoPath, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	changed := false
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 2
		changed = true
	}
	if cfg.Meta.Name == "" {
		cfg.Meta.Name = filepath.Base(repoPath)
		changed = true
	}
	if cfg.Meta.Added == "" {
		cfg.Meta.Added = time.Now().UTC().Format(time.RFC3339)
		changed = true
	}
	if cfg.Meta.AddedBy == "" {
		cfg.Meta.AddedBy = "unknown"
		if name, err := loadMachineName(); err == nil {
			cfg.Meta.AddedBy = name
		}
		changed = true
	}
	if cfg.Sync.SyncthingFolderID == "" {
		cfg.Sync.SyncthingFolderID = folderIDForRepo(repoPath)
		changed = true
	}
	if changed {
		if err := config.WriteRepoConfigV2(repoPath, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func folderIDForRepo(repoPath string) string {
	identity := gitRemoteOrigin(repoPath)
	if identity == "" {
		identity = repoPath
	}
	sum := sha256.Sum256([]byte(identity))
	return "dk-" + slug(filepath.Base(repoPath)) + "-" + fmt.Sprintf("%x", sum[:4])
}

func gitRemoteOrigin(repoPath string) string {
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func slug(raw string) string {
	raw = strings.TrimSuffix(strings.ToLower(raw), ".git")
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "repo"
	}
	return out
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
// instance (if available) and state.toml. v0.9.6-compatible signature; kept
// for the one-shot `dotkeeper reconcile` command which doesn't run a tracker.
func buildObservedProvider(statePath string, activity reconcile.ActivityQuerier) reconcile.ObservedProvider {
	if key, err := engine().APIKey(); err == nil {
		return reconcile.NewObservedProviderWithActivity(stclient.New(key), statePath, activity)
	}
	return reconcile.NewObservedProviderWithActivity(nil, statePath, activity)
}

// buildObservedProviderFull is the v0.9.7 entry point that wires
// the full ObservedProviderInputs into a provider. Used by the
// long-running daemon where every input is available; the one-shot
// CLI keeps using buildObservedProvider.
func buildObservedProviderFull(statePath string, inputs reconcile.ObservedProviderInputs) reconcile.ObservedProvider {
	if key, err := engine().APIKey(); err == nil {
		return reconcile.NewObservedProviderFull(stclient.New(key), statePath, inputs)
	}
	return reconcile.NewObservedProviderFull(nil, statePath, inputs)
}

// healthForReconcile is the narrow interface the daemon-side
// startReconcileDaemon needs from the watchhealth tracker. The
// concrete *watchhealth.Tracker satisfies it (via the wrapper
// below). Defining it here rather than importing watchhealth into
// reconcile keeps the package graph one-way: reconcile depends on
// nothing platform-specific.
type healthForReconcile interface {
	reconcile.HealthQuerier
	reconcile.HealthResetter
}

// healthResetter returns the HealthResetter interface view of h,
// or nil when h is nil. Lets us pass it to RealApplier.Health
// without an explicit nil check at the call site.
func healthResetter(h healthForReconcile) reconcile.HealthResetter {
	if h == nil {
		return nil
	}
	return h
}

// healthAdapter bridges *watchhealth.Tracker (returns watchhealth.Status)
// to the reconcile-side interfaces (returns reconcile.FolderHealth).
// Kept at the daemon edge so the reconcile package never imports
// watchhealth and vice-versa.
type healthAdapter struct {
	tracker *watchhealth.Tracker
}

func newHealthAdapter(tr *watchhealth.Tracker) *healthAdapter {
	if tr == nil {
		return nil
	}
	return &healthAdapter{tracker: tr}
}

func (h *healthAdapter) StatusForReconcile(root string) (reconcile.FolderHealth, bool) {
	st, ok := h.tracker.Status(root)
	if !ok {
		return reconcile.FolderHealth{}, false
	}
	return reconcile.FolderHealth{
		FilesystemReliable:  st.Kind == watchhealth.FilesystemReliable,
		OverflowSeen:        st.OverflowSeen,
		WatchLimitHit:       st.WatchLimitHit,
		LastReliableEventAt: st.LastReliableEventAt,
	}, true
}

func (h *healthAdapter) Reset(root string) {
	h.tracker.Reset(root)
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
//
// Optional v0.9.6/v0.9.7/v1.0.0 inputs:
//
//   - activityTracker: feeds LastActivityByPath and the immediate-
//     unpause hint channel.
//   - health: per-folder filesystem classification + overflow flags.
//   - wake: one-shot suspend/resume signal.
//   - rescans: in-memory last-rescan-per-folder log used by the
//     backstop check.
//   - propagator: v1.0.0 transport-manager-driven peer
//     propagation. When non-nil, every successful auto-commit
//     fans out to every paired peer via Manager.Route.
//
// Any of these may be nil; reconcile/Diff degrades gracefully (no
// rescan emissions, no auto-pause emissions, no transport-driven
// peer push), preserving the previous-release baseline.
func startReconcileDaemon(
	ctx context.Context,
	logger *slog.Logger,
	activityTracker *activity.Tracker,
	health healthForReconcile,
	wake reconcileWakeFlag,
	rescans *rescanLog,
	propagator reconcile.CommitPropagator,
) {
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

	// pprof listener: opt-in via machine.toml's [debug] pprof_address.
	// Started early so any subsequent perf work (and the rest of this
	// startup function) is fully observable.
	startPprofListener(ctx, machine.Debug.PprofAddress, logger)

	desired := reconcile.NewDesiredProvider(machinePath, statePath)

	// Build the full ObservedProviderInputs. Each input is converted
	// from the daemon-side concrete type to the narrow reconcile
	// interface here so the reconcile package doesn't depend on the
	// concrete activity/watchhealth/wake packages.
	inputs := reconcile.ObservedProviderInputs{}
	if activityTracker != nil {
		inputs.Activity = activityTracker
	}
	if health != nil {
		inputs.Health = health
	}
	if wake != nil {
		inputs.Wake = wake
	}
	if rescans != nil {
		inputs.LastRescans = rescans
	}
	observed := buildObservedProviderFull(statePath, inputs)

	// Wire up Syncthing client for the applier.
	var stClient *stclient.Client
	if key, err := engine().APIKey(); err == nil {
		stClient = stclient.New(key)
		// One-shot migration for installs created before v1.1.14:
		// dotkeeper used to set autoAcceptFolders=true on every
		// device, which combined with the missing default folder
		// path produced a continuous "Failed to auto-accept folder
		// due to path conflict" ERROR storm for every folder a peer
		// offered that the local side hadn't subscribed to. Folder
		// membership is now opt-in per machine.
		if n, err := stClient.MigrateDisableAutoAcceptFolders(); err != nil {
			logger.WarnContext(ctx, "auto-accept migration failed",
				"err", err)
		} else if n > 0 {
			logger.InfoContext(ctx, "migrated autoAcceptFolders=false",
				"devices", n)
		}
	}

	applier := &reconcile.RealApplier{
		ST:                 stClient,
		Logger:             logger,
		Health:             healthResetter(health),
		LastRescanRecorder: rescans,
		Propagator:         propagator,
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

	// Peer-presence tracker: refresh state.LastSeenPeers from
	// Syncthing's live connection table on every reconcile tick.
	// Decoupled from reconcile itself (own goroutine) so a slow
	// Syncthing API call can't stall the reconcile cycle, and a
	// reconcile failure can't drop a presence update. Best-effort;
	// failures log and skip rather than crash. Started only when
	// the Syncthing client is wired up — without it the tracker is
	// a no-op that would burn ticks.
	if stClient != nil {
		go runPeerPresenceTracker(ctx, stClient, reconcileInterval, logger)
	}

	startReconcileLoop(ctx, r, reconcileInterval, watchPaths, logger)

	// Fan activity hints into the reconcile-trigger channel so a
	// paused folder unpauses on the first user touch instead of
	// waiting up to ReconcileInterval. The reconcile loop's
	// single-slot pending channel coalesces bursts naturally.
	// startReconcileLoop above doesn't expose its requestReconcile
	// closure, so we use the same mechanism it does: a synthetic
	// touch on machine.toml is the documented "ask reconcile to
	// run" signal that the fsnotify branch already debounces and
	// triggers on. Cheaper than threading another channel through
	// the loop's signature.
	if activityTracker != nil {
		go forwardActivityHintsToReconcile(ctx, activityTracker.Hints(), machinePath, logger)
	}
}

// forwardActivityHintsToReconcile drains the activity tracker's
// Hints channel and, on each event, touches machine.toml — which the
// reconcile loop's fsnotify watcher debounces into a reconcile
// trigger. Coalescing happens in the reconcile loop's single-slot
// pending channel, so a burst of editor saves produces at most one
// reconcile per debounce window.
//
// We touch via os.Chtimes rather than rewriting the file, so the
// activity fan-out can never corrupt machine.toml even on disk
// errors.
func forwardActivityHintsToReconcile(ctx context.Context, hints <-chan string, machinePath string, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-hints:
			if !ok {
				return
			}
			now := time.Now()
			if err := os.Chtimes(machinePath, now, now); err != nil {
				logger.DebugContext(ctx, "activity-hint chtimes failed", "err", err)
			}
		}
	}
}

// startReconcileLoop wires reconcile triggers and a single serialised worker.
// Triggers:
//  1. An initial reconcile fired once on entry (so a fresh daemon doesn't
//     wait the full ReconcileInterval before doing anything).
//  2. A time.Ticker at reconcileInterval (safety net).
//  3. fsnotify watches over every directory under each scan root, plus
//     state.toml and machine.toml. New directories created under a scan
//     root are added to the watch on the fly. Events on .dotkeeper.toml,
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
// directories created under any watched root and debounces .dotkeeper.toml /
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
	// then drops a .dotkeeper.toml inside it.
	if event.Op&fsnotify.Create == fsnotify.Create && watcher != nil {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			addWatchRecursive(watcher, event.Name, logger)
			// Don't return — a new directory by itself doesn't warrant a
			// reconcile (no .dotkeeper.toml yet), but we also don't want the
			// basename filter below to skip it; just fall through.
		}
	}

	// We only reconcile in response to .dotkeeper.toml / state.toml / machine.toml
	// changes. Directory events and unrelated files are ignored.
	base := filepath.Base(event.Name)
	if base != config.RepoConfigFileName && base != "machine.toml" && base != "state.toml" {
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

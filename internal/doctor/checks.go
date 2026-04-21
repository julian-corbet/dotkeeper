// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/conflict"
	"github.com/julian-corbet/dotkeeper/internal/service"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// STClient is the subset of stclient.Client the doctor checks depend on.
// Using an interface lets tests substitute a deterministic fake and
// keeps the doctor package free of network dependencies during unit
// testing.
type STClient interface {
	Ping() error
	GetStatus() (*stclient.SystemStatus, error)
	GetConfig() (map[string]any, error)
	GetConnections() (*stclient.Connections, error)
	GetFolderStatus(folderID string) (*stclient.FolderStatus, error)
}

// GitRunner runs `git` on behalf of the git-remote reachability check.
// The concrete implementation shells out via os/exec, but the interface
// lets tests stub in deterministic results — reaching a real remote
// across the network from CI is both slow and flaky.
type GitRunner interface {
	// LsRemote runs `git -C dir ls-remote --heads origin HEAD`. Returns
	// (combined stdout+stderr, error). ctx must be honoured.
	LsRemote(ctx context.Context, dir string) (string, error)
}

// ExecGitRunner is the production GitRunner that calls out to `git`.
type ExecGitRunner struct{}

// LsRemote runs git ls-remote with a short per-call deadline derived
// from ctx. Callers decide the actual timeout — the check passes
// context.WithTimeout(ctx, 5*time.Second).
func (ExecGitRunner) LsRemote(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-remote", "--heads", "origin", "HEAD")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- 1. version -------------------------------------------------------

// VersionCheck reports the running binary's version and commit. Always
// OK — the point is to make the doctor output self-describing when it
// gets pasted into an issue report.
type VersionCheck struct {
	Version string
	Commit  string
}

func (VersionCheck) Name() string { return "version" }
func (c VersionCheck) Run(_ context.Context) Result {
	return Result{
		Name:    "version",
		Outcome: OK,
		Detail:  fmt.Sprintf("dotkeeper %s (%s) on %s/%s", c.Version, c.Commit, runtime.GOOS, runtime.GOARCH),
	}
}

// --- 2. config --------------------------------------------------------

// ConfigCheck loads machine.toml and config.toml. Fails if either is
// missing or unparseable (dotkeeper can't function without them). Warns
// when machine.toml's name is not registered in the shared config's
// machines table — usually a sign that `dotkeeper pair` hasn't run yet
// or the config sync hasn't caught up.
type ConfigCheck struct {
	// Loader overrides let tests inject fake configs; when nil, the
	// real on-disk loaders are used.
	LoadMachine func() (*config.MachineConfig, error)
	LoadShared  func() (*config.SharedConfig, error)
}

func (ConfigCheck) Name() string { return "config" }
func (c ConfigCheck) Run(_ context.Context) Result {
	loadM := c.LoadMachine
	if loadM == nil {
		loadM = config.LoadMachineConfig
	}
	loadS := c.LoadShared
	if loadS == nil {
		loadS = config.LoadSharedConfig
	}

	m, err := loadM()
	if err != nil {
		return Result{Name: "config", Outcome: Fail, Detail: "machine.toml: " + err.Error(), Hint: "check file contents at " + config.MachineConfigPath()}
	}
	if m == nil {
		return Result{Name: "config", Outcome: Fail, Detail: "machine.toml missing", Hint: "run 'dotkeeper init'"}
	}

	cfg, err := loadS()
	if err != nil {
		return Result{Name: "config", Outcome: Fail, Detail: "config.toml: " + err.Error(), Hint: "check file contents at " + config.SharedConfigPath()}
	}
	if cfg == nil {
		return Result{Name: "config", Outcome: Fail, Detail: "config.toml missing", Hint: "run 'dotkeeper init' or join an existing setup"}
	}

	// Warn-path: machine identity not in shared config yet.
	found := false
	for _, entry := range cfg.Machines {
		if entry.Hostname == m.Name {
			found = true
			break
		}
	}
	if !found {
		return Result{
			Name:    "config",
			Outcome: Warn,
			Detail:  fmt.Sprintf("machine %q not in config registry (%d repos tracked)", m.Name, len(cfg.Repos)),
			Hint:    "run 'dotkeeper pair' — shared config may not be synced yet",
		}
	}
	return Result{
		Name:    "config",
		Outcome: OK,
		Detail:  fmt.Sprintf("machine %q (slot %d), %d managed folders", m.Name, m.Slot, len(cfg.Repos)),
	}
}

// --- 3. service -------------------------------------------------------

// ServiceCheck queries the platform service manager for the Syncthing
// unit state. OK when active/running, Warn when inactive (user hasn't
// started it), Fail when failed/error.
//
// When the platform has no service manager (noop backend) or the unit
// state is simply unavailable, the check reports Warn with a note — it
// can't verify the service, but that's a dotkeeper configuration gap,
// not a Syncthing failure.
type ServiceCheck struct {
	Manager service.Manager
}

func (ServiceCheck) Name() string { return "service" }
func (c ServiceCheck) Run(_ context.Context) Result {
	if c.Manager == nil {
		return Result{Name: "service", Outcome: Warn, Detail: "no service manager available (manual mode)"}
	}

	// Prefer the rich status when the backend exposes it. Only systemd
	// does today; cron/launchd/windows fall back to the boolean API.
	if rich, ok := c.Manager.(interface {
		SyncthingStatus() service.SyncthingUnitStatus
	}); ok {
		st := rich.SyncthingStatus()
		return interpretSyncthingStatus(c.Manager.Name(), st)
	}

	if c.Manager.IsSyncthingRunning() {
		return Result{
			Name:    "service",
			Outcome: OK,
			Detail:  fmt.Sprintf("dotkeeper-syncthing running (%s)", c.Manager.Name()),
		}
	}
	return Result{
		Name:    "service",
		Outcome: Warn,
		Detail:  fmt.Sprintf("dotkeeper-syncthing not running (%s)", c.Manager.Name()),
		Hint:    "start it with 'dotkeeper start' or your platform's service command",
	}
}

func interpretSyncthingStatus(backend string, st service.SyncthingUnitStatus) Result {
	switch st.Active {
	case "active":
		detail := fmt.Sprintf("dotkeeper-syncthing.service active (%s)", st.Sub)
		if !st.Since.IsZero() {
			detail = fmt.Sprintf("dotkeeper-syncthing.service active (%s since %s)",
				st.Sub, st.Since.Format("2006-01-02 15:04:05"))
		}
		return Result{Name: "service", Outcome: OK, Detail: detail}
	case "failed":
		return Result{
			Name:    "service",
			Outcome: Fail,
			Detail:  "dotkeeper-syncthing.service failed",
			Hint:    "check logs: journalctl --user -u dotkeeper-syncthing.service",
		}
	case "inactive", "deactivating":
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  "dotkeeper-syncthing.service inactive",
			Hint:    "start it: systemctl --user start dotkeeper-syncthing.service",
		}
	case "activating":
		return Result{Name: "service", Outcome: Warn, Detail: "dotkeeper-syncthing.service starting…"}
	case "":
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  fmt.Sprintf("dotkeeper-syncthing unit status unknown (%s backend)", backend),
		}
	default:
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  fmt.Sprintf("dotkeeper-syncthing.service %s", st.Active),
		}
	}
}

// --- 4. syncthing API --------------------------------------------------

// SyncthingAPICheck pings the Syncthing REST API. OK when /rest/system/ping
// returns 200; Fail otherwise. This check is a prerequisite for peers
// and folders — when it fails, those later checks will fail too but
// the hint on *this* check is the actionable one.
type SyncthingAPICheck struct {
	Client STClient
}

func (SyncthingAPICheck) Name() string { return "syncthing API" }
func (c SyncthingAPICheck) Run(_ context.Context) Result {
	if c.Client == nil {
		return Result{Name: "syncthing API", Outcome: Fail, Detail: "client not available", Hint: "dotkeeper not initialised — run 'dotkeeper init'"}
	}
	if err := c.Client.Ping(); err != nil {
		return Result{
			Name:    "syncthing API",
			Outcome: Fail,
			Detail:  fmt.Sprintf("%s unreachable (%v)", stclient.APIAddress, err),
			Hint:    "is dotkeeper-syncthing.service running?",
		}
	}
	return Result{
		Name:    "syncthing API",
		Outcome: OK,
		Detail:  "reachable at " + stclient.APIAddress,
	}
}

// --- 5. peers ---------------------------------------------------------

// PeersCheck compares the peer list from the shared config against
// the Syncthing /rest/system/connections payload. OK when every
// configured peer (not counting this machine) is connected; Warn when
// any are offline — peers can legitimately be offline; Fail only when
// the API call itself fails.
type PeersCheck struct {
	Client     STClient
	LoadShared func() (*config.SharedConfig, error)
}

func (PeersCheck) Name() string { return "peers" }
func (c PeersCheck) Run(_ context.Context) Result {
	if c.Client == nil {
		return Result{Name: "peers", Outcome: Fail, Detail: "client not available"}
	}
	loadS := c.LoadShared
	if loadS == nil {
		loadS = config.LoadSharedConfig
	}
	cfg, err := loadS()
	if err != nil || cfg == nil {
		return Result{Name: "peers", Outcome: Warn, Detail: "config unavailable; cannot list expected peers"}
	}

	status, err := c.Client.GetStatus()
	if err != nil {
		return Result{Name: "peers", Outcome: Fail, Detail: "cannot read system status: " + err.Error()}
	}
	myID := status.MyID

	conns, err := c.Client.GetConnections()
	if err != nil {
		return Result{Name: "peers", Outcome: Fail, Detail: "cannot read connections: " + err.Error()}
	}

	// Expected peers = every registered machine except this one.
	type peer struct {
		key, host, id string
	}
	var expected []peer
	for k, m := range cfg.Machines {
		if m.SyncthingID == "" || m.SyncthingID == myID {
			continue
		}
		expected = append(expected, peer{key: k, host: m.Hostname, id: m.SyncthingID})
	}
	if len(expected) == 0 {
		return Result{Name: "peers", Outcome: OK, Detail: "no peers configured (single-machine setup)"}
	}

	var connectedNames []string
	var offlineNames []string
	for _, p := range expected {
		conn, ok := conns.Connections[p.id]
		if ok && conn.Connected {
			connectedNames = append(connectedNames, p.host)
		} else {
			offlineNames = append(offlineNames, p.host)
		}
	}

	detail := fmt.Sprintf("%d/%d connected (%s)", len(connectedNames), len(expected), strings.Join(connectedNames, ", "))
	if len(offlineNames) == 0 {
		return Result{Name: "peers", Outcome: OK, Detail: detail}
	}
	return Result{
		Name:    "peers",
		Outcome: Warn,
		Detail:  fmt.Sprintf("%d/%d connected, offline: %s", len(connectedNames), len(expected), strings.Join(offlineNames, ", ")),
		Hint:    "peers may be legitimately offline; verify on the peer machine",
	}
}

// --- 6. folders -------------------------------------------------------

// FoldersCheck walks every folder Syncthing knows about and classifies
// its state. OK when every folder is idle; Warn on transient syncing
// or scanning; Fail on error/stopped states which block real sync.
type FoldersCheck struct {
	Client STClient
}

func (FoldersCheck) Name() string { return "folders" }
func (c FoldersCheck) Run(_ context.Context) Result {
	if c.Client == nil {
		return Result{Name: "folders", Outcome: Fail, Detail: "client not available"}
	}
	cfg, err := c.Client.GetConfig()
	if err != nil {
		return Result{Name: "folders", Outcome: Fail, Detail: "cannot read folders: " + err.Error()}
	}
	rawFolders, _ := cfg["folders"].([]any)
	if len(rawFolders) == 0 {
		return Result{Name: "folders", Outcome: Warn, Detail: "no folders configured"}
	}

	var idle, syncing, scanning, errored, stopped, other int
	var errorDetails []string

	for _, f := range rawFolders {
		fm, _ := f.(map[string]any)
		id, _ := fm["id"].(string)
		if id == "" {
			continue
		}
		st, err := c.Client.GetFolderStatus(id)
		if err != nil {
			errored++
			errorDetails = append(errorDetails, id+" (API error)")
			continue
		}
		switch st.State {
		case "idle":
			idle++
		case "syncing":
			syncing++
		case "scanning", "scan-waiting", "sync-waiting", "sync-preparing":
			scanning++
		case "error":
			errored++
			errorDetails = append(errorDetails, id)
		case "stopped":
			stopped++
			errorDetails = append(errorDetails, id+" (stopped)")
		default:
			other++
		}
	}

	detail := fmt.Sprintf("%d idle, %d syncing, %d scanning, %d errors",
		idle, syncing, scanning, errored+stopped)

	switch {
	case errored+stopped > 0:
		return Result{
			Name:    "folders",
			Outcome: Fail,
			Detail:  detail + " — " + strings.Join(errorDetails, ", "),
			Hint:    "check Syncthing web UI or journalctl for folder errors",
		}
	case syncing+scanning > 0:
		return Result{Name: "folders", Outcome: Warn, Detail: detail}
	default:
		return Result{Name: "folders", Outcome: OK, Detail: detail}
	}
}

// --- 7. git remotes ---------------------------------------------------

// GitRemotesCheck runs `git ls-remote --heads origin HEAD` on every
// configured repo with git=true. OK when all reachable, Warn on
// timeouts (network flake isn't dotkeeper's fault), Fail on auth or
// unknown-host errors.
//
// Remotes are checked in parallel with a shared short deadline so the
// whole check is bounded even if every repo goes unreachable.
type GitRemotesCheck struct {
	Runner     GitRunner
	LoadShared func() (*config.SharedConfig, error)
	Timeout    time.Duration
}

func (GitRemotesCheck) Name() string { return "git remotes" }
func (c GitRemotesCheck) Run(ctx context.Context) Result {
	loadS := c.LoadShared
	if loadS == nil {
		loadS = config.LoadSharedConfig
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	cfg, err := loadS()
	if err != nil || cfg == nil {
		return Result{Name: "git remotes", Outcome: Warn, Detail: "config unavailable"}
	}

	var gitRepos []config.RepoEntry
	for _, r := range cfg.Repos {
		if r.Git {
			gitRepos = append(gitRepos, r)
		}
	}
	if len(gitRepos) == 0 {
		return Result{Name: "git remotes", Outcome: OK, Detail: "no git-tracked repos"}
	}

	var reachable, timeouts, failed int
	var failedDetails []string

	// Bounded parallelism keeps a hung remote from slowing the others.
	type result struct {
		name, out string
		err       error
		timedOut  bool
	}
	results := make(chan result, len(gitRepos))
	for _, repo := range gitRepos {
		go func(repo config.RepoEntry) {
			path := config.ExpandPath(repo.Path)
			if _, err := os.Stat(path); err != nil {
				results <- result{name: repo.Name, err: fmt.Errorf("path missing: %s", path)}
				return
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			out, err := runner.LsRemote(cctx, path)
			timedOut := cctx.Err() == context.DeadlineExceeded
			results <- result{name: repo.Name, out: out, err: err, timedOut: timedOut}
		}(repo)
	}

	for i := 0; i < len(gitRepos); i++ {
		r := <-results
		switch {
		case r.err == nil:
			reachable++
		case r.timedOut:
			timeouts++
			failedDetails = append(failedDetails, r.name+" (timeout)")
		default:
			// Heuristically identify auth / unknown-host cases so the
			// hint is targeted. The output of git is stable enough for
			// this to be robust in practice.
			lc := strings.ToLower(r.out + " " + r.err.Error())
			switch {
			case strings.Contains(lc, "permission denied"),
				strings.Contains(lc, "authentication failed"),
				strings.Contains(lc, "could not read username"):
				failed++
				failedDetails = append(failedDetails, r.name+" (auth)")
			case strings.Contains(lc, "could not resolve host"),
				strings.Contains(lc, "name or service not known"):
				failed++
				failedDetails = append(failedDetails, r.name+" (dns)")
			default:
				failed++
				failedDetails = append(failedDetails, r.name)
			}
		}
	}

	detail := fmt.Sprintf("%d reachable, %d timeout, %d failed",
		reachable, timeouts, failed)
	switch {
	case failed > 0:
		return Result{
			Name:    "git remotes",
			Outcome: Fail,
			Detail:  detail + " — " + strings.Join(failedDetails, ", "),
			Hint:    "check SSH keys / network / remote URL for the failing repo(s)",
		}
	case timeouts > 0:
		return Result{
			Name:    "git remotes",
			Outcome: Warn,
			Detail:  detail + " — " + strings.Join(failedDetails, ", "),
			Hint:    "timeouts may indicate a network flake; try again later",
		}
	default:
		return Result{
			Name:    "git remotes",
			Outcome: OK,
			Detail:  fmt.Sprintf("%d reachable", reachable),
		}
	}
}

// --- 8. backup timer --------------------------------------------------

// BackupTimerCheck queries the platform service manager for the git
// backup timer. OK when active (with next-run time when available),
// Warn when inactive.
type BackupTimerCheck struct {
	Manager service.Manager
}

func (BackupTimerCheck) Name() string { return "backup timer" }
func (c BackupTimerCheck) Run(_ context.Context) Result {
	if c.Manager == nil {
		return Result{Name: "backup timer", Outcome: Warn, Detail: "no service manager available"}
	}
	if !c.Manager.IsTimerActive() {
		return Result{
			Name:    "backup timer",
			Outcome: Warn,
			Detail:  "inactive",
			Hint:    "run 'dotkeeper install-timer'",
		}
	}
	// Rich next-run info when the backend provides it (systemd today).
	if rich, ok := c.Manager.(interface{ TimerNext() service.TimerNextRun }); ok {
		next := rich.TimerNext()
		if next.Raw != "" {
			return Result{Name: "backup timer", Outcome: OK, Detail: "active, next run " + next.Raw}
		}
	}
	return Result{Name: "backup timer", Outcome: OK, Detail: "active"}
}

// --- 9. conflicts -----------------------------------------------------

// ConflictsCheck scans every managed folder for pending sync-conflict
// files. OK when zero; Warn when any exist.
//
// The check takes the Scanner as an injectable function so tests can
// exercise it without preparing a real folder tree on disk.
type ConflictsCheck struct {
	LoadShared func() (*config.SharedConfig, error)
	Scanner    func(root string) ([]conflict.Conflict, error)
}

func (ConflictsCheck) Name() string { return "conflicts" }
func (c ConflictsCheck) Run(_ context.Context) Result {
	loadS := c.LoadShared
	if loadS == nil {
		loadS = config.LoadSharedConfig
	}
	scan := c.Scanner
	if scan == nil {
		scan = conflict.Scan
	}

	cfg, err := loadS()
	if err != nil || cfg == nil {
		return Result{Name: "conflicts", Outcome: Warn, Detail: "config unavailable"}
	}

	roots := managedFolderPaths(cfg)
	var total int
	for _, root := range roots {
		found, err := scan(root)
		if err != nil {
			// Skip unreadable roots — the scanner already tolerates
			// most errors; anything it does surface is an edge case
			// (unreadable root path) rather than a conflict-count
			// problem.
			continue
		}
		total += len(found)
	}
	if total == 0 {
		return Result{Name: "conflicts", Outcome: OK, Detail: "0 pending"}
	}
	return Result{
		Name:    "conflicts",
		Outcome: Warn,
		Detail:  fmt.Sprintf("%d pending", total),
		Hint:    "run 'dotkeeper conflict list' to inspect",
	}
}

// managedFolderPaths mirrors the helper in cmd/dotkeeper/main.go — it
// returns every managed folder root (the config dir + every repo path)
// for scanning purposes. Duplicating it here keeps the doctor package
// free of dependencies on the CLI's internals.
func managedFolderPaths(cfg *config.SharedConfig) []string {
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
	add(config.ConfigDir())
	for _, r := range cfg.Repos {
		add(r.Path)
	}
	return out
}

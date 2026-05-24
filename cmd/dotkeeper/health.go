// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// liveConnectionsProvider returns the set of peer device IDs that
// Syncthing currently reports as connected, with the observation
// timestamp. Returns nil/nil when no API client is available — the
// health command must degrade gracefully when Syncthing is down
// or its API key isn't readable.
//
// Indirection via a package-level var (rather than a direct call
// to apiClient()) lets tests inject a stub without spinning up a
// real Syncthing instance.
var liveConnectionsProvider = func() (map[string]time.Time, error) {
	eng := engine()
	key, err := eng.APIKey()
	if err != nil {
		// API key unreadable → daemon almost certainly not
		// running. Fall back to whatever state.toml has.
		return nil, nil //nolint:nilerr // intentional graceful degradation
	}
	c := stclient.New(key)
	conns, err := c.GetConnections()
	if err != nil {
		return nil, nil //nolint:nilerr // same: daemon unreachable, not a hard failure
	}
	out := make(map[string]time.Time)
	now := time.Now()
	for deviceID, conn := range conns.Connections {
		if conn.Connected {
			out[deviceID] = now
		}
	}
	return out, nil
}

// healthCmd returns the `dotkeeper health` subcommand: an
// at-a-glance operational dashboard that surfaces silent
// degradation. The existing `dotkeeper status` answers "is this
// configured correctly?"; `health` answers "is it actually
// working?" — repo freshness, recent reconcile/conflict activity,
// per-peer last-seen timestamps. Designed to be the command you
// run when an operation that should have happened didn't.
//
// JSON mode (--json) emits a machine-readable HealthReport so a
// shell wrapper or systemd timer can alert on degraded state
// without parsing pretty-printed output. The struct fields are
// the API surface; field names are kebab-case in JSON to match
// Prometheus-style conventions and stable across releases.
func healthCmd() *cobra.Command {
	var jsonOut bool
	var noLogScan bool
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Operational health snapshot — repo freshness, peer activity, recent errors",
		Long: `Show operational health of the dotkeeper daemon.

Unlike 'dotkeeper status' (which reports configuration), 'health' reports
whether the configured state is actually working: repos are being backed up
on schedule, peers are being seen, conflict-resolver isn't accumulating
auto-resolves, recent log errors are within expectations.

Exit codes:
  0  — healthy: no stale repos, no recent errors
  1  — warnings present (stale repos, conflict-resolver activity, log errors)
  2  — failure to read state (machine.toml / state.toml unreadable)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := collectHealth(noLogScan)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeHealthJSON(cmd.OutOrStdout(), rep)
			}
			writeHealthText(cmd.OutOrStdout(), rep)
			if rep.degraded() {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable text")
	cmd.Flags().BoolVar(&noLogScan, "no-log-scan", false, "Skip the syncthing.log tail (faster, but omits Recent-activity section)")
	return cmd
}

// HealthReport is the data shape backing `dotkeeper health`.
// Exported so external tooling can `dotkeeper health --json | jq`
// against stable field names.
type HealthReport struct {
	GeneratedAt time.Time `json:"generated-at"`
	Machine     struct {
		Name string `json:"name"`
		Slot uint   `json:"slot"`
	} `json:"machine"`
	Repos struct {
		Total           int `json:"total"`
		FreshLast24h    int `json:"fresh-last-24h"`
		StaleOneToSeven int `json:"stale-1-to-7-days"`
		StaleOverSeven  int `json:"stale-over-7-days"`
		// Idle counts repos whose backup is "stale" by age but
		// where git itself hasn't changed since the backup — i.e.
		// the backup is correctly current, the repo just isn't
		// being worked on. Separated from Stale* so health doesn't
		// flag long-dormant projects as degraded.
		Idle             int             `json:"idle"`
		NeverBackedUp    int             `json:"never-backed-up"`
		OldestBackup     []RepoBackupAge `json:"oldest-backups,omitempty"`
		NeverBackedNames []string        `json:"never-backed-names,omitempty"`
		// LaggingBackups names repos where git activity is more
		// recent than dotkeeper's last backup — the actually-bad
		// case, distinct from idle.
		LaggingBackups []RepoLaggingBackup `json:"lagging-backups,omitempty"`
		_              struct{}            `json:"-"`
	} `json:"repos"`
	Peers struct {
		Known    int            `json:"known"`
		LastSeen []PeerLastSeen `json:"last-seen,omitempty"`
	} `json:"peers"`
	RecentActivity *RecentActivity `json:"recent-activity,omitempty"`
}

// RepoBackupAge is one row of the "oldest backups" table.
type RepoBackupAge struct {
	Path  string    `json:"path"`
	Since time.Time `json:"since"`
	AgeS  float64   `json:"age-seconds"`
}

// RepoLaggingBackup is one row of the "lagging backups" table —
// repos where the local git HEAD has activity newer than
// dotkeeper's last recorded backup. These are the genuine
// operational concerns (versus repos that are simply dormant).
type RepoLaggingBackup struct {
	Path       string    `json:"path"`
	GitMTime   time.Time `json:"git-mtime"`
	BackupAt   time.Time `json:"backup-at"`
	LagSeconds float64   `json:"lag-seconds"`
}

// PeerLastSeen is one row of the peer last-seen table.
type PeerLastSeen struct {
	Name     string    `json:"name"`
	Since    time.Time `json:"since"`
	AgeS     float64   `json:"age-seconds"`
	DeviceID string    `json:"device-id"`
}

// RecentActivity summarises syncthing.log entries from the last
// 24h. Nil when the user passed --no-log-scan or the log file is
// missing.
type RecentActivity struct {
	WindowStart      time.Time `json:"window-start"`
	WindowEnd        time.Time `json:"window-end"`
	BytesScanned     int64     `json:"bytes-scanned"`
	ConflictResolved int       `json:"conflict-resolved"`
	PushFailures     int       `json:"push-failures"`
	WarnCount        int       `json:"warn-count"`
	ErrorCount       int       `json:"error-count"`
}

// degraded reports whether any field crosses the "ping the
// operator" threshold. Used to set the exit code so a wrapping
// systemd timer (or `dotkeeper health || mail-me`) can react.
//
// IMPORTANT: a repo is only degraded when git activity has
// outpaced backup activity ("lagging"), not just because the
// backup is old in absolute terms. A long-dormant project that
// hasn't been touched in months should NOT trigger a degraded
// status — there's nothing for dotkeeper to back up. The v1.1.3
// version of this check confused operators by flagging archived
// projects as failures; v1.1.4 corrects it.
func (r *HealthReport) degraded() bool {
	if len(r.Repos.LaggingBackups) > 0 || r.Repos.NeverBackedUp > 0 {
		return true
	}
	if r.RecentActivity != nil {
		if r.RecentActivity.PushFailures > 0 || r.RecentActivity.ErrorCount > 0 {
			return true
		}
	}
	return false
}

// gitMTimeProvider returns the timestamp of the most recent
// commit on HEAD for the repo at path, or the zero time when the
// path isn't a working tree or git fails. The default uses
// `git log -1 --format=%ct`; tests inject a stub to avoid forking
// real git on synthetic fixtures.
//
// The "git tree" notion here is intentionally narrow: we care
// only whether the user has authored something newer than the
// last backup. Uncommitted-but-staged changes don't count
// (dotkeeper's auto-commit captures those into HEAD anyway).
var gitMTimeProvider = func(path string) time.Time {
	cmd := exec.CommandContext(context.Background(), "git", "-C", path, "log", "-1", "--format=%ct")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return time.Time{}
	}
	var sec int64
	if _, err := fmt.Sscanf(s, "%d", &sec); err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// logTailBytes bounds how much of syncthing.log we scan. 4MB is
// roughly the last day of activity on a healthy install and
// generous enough that even noisy operation stays inside the
// window. Bigger logs (the production fleet saw 1.3GB) would
// otherwise dominate the command's runtime.
const logTailBytes int64 = 4 << 20

// collectHealth gathers all health data from on-disk state. No
// daemon interaction — works whether the daemon is running or
// not, which is exactly what an operator needs when triaging
// "is dotkeeper alive?"
func collectHealth(noLogScan bool) (*HealthReport, error) {
	machine, err := config.LoadMachineConfigV2()
	if err != nil {
		return nil, fmt.Errorf("read machine.toml: %w", err)
	}
	state, err := config.LoadStateV2()
	if err != nil {
		return nil, fmt.Errorf("read state.toml: %w", err)
	}

	now := time.Now()
	rep := &HealthReport{GeneratedAt: now}
	if machine != nil {
		rep.Machine.Name = machine.Name
		rep.Machine.Slot = machine.Slot
	}

	if state != nil {
		summariseRepos(state, now, rep)
	}
	// Peers are declared in machine.toml (authoritative roster) but
	// last-seen timestamps live in state.toml (runtime observation).
	// Combine both so the output names every configured peer, even
	// ones that have never been seen.
	summarisePeers(machine, state, now, rep)

	if !noLogScan {
		if act, err := scanRecentActivity(now); err == nil {
			rep.RecentActivity = act
		}
		// scan failures are non-fatal — the report just omits the
		// section. health stays useful when the log is missing or
		// permissions are wrong.
	}

	return rep, nil
}

func summariseRepos(state *config.StateV2, now time.Time, rep *HealthReport) {
	type ageRow struct {
		path string
		t    time.Time
	}
	// laggingGrace is the threshold above which a git-newer-than-
	// backup gap is considered an actual lag worth flagging. Below
	// this the user is probably mid-edit and dotkeeper just hasn't
	// finished its next reconcile cycle yet — false-positive
	// degradation in that window erodes trust in the signal.
	const laggingGrace = 10 * time.Minute

	var ages []ageRow
	for path, obs := range state.ObservedRepos {
		rep.Repos.Total++
		if obs.LastBackupAt.IsZero() {
			rep.Repos.NeverBackedUp++
			rep.Repos.NeverBackedNames = append(rep.Repos.NeverBackedNames, path)
			continue
		}
		ages = append(ages, ageRow{path: path, t: obs.LastBackupAt})
		age := now.Sub(obs.LastBackupAt)

		// Lagging vs idle distinction: a "stale" backup is only a
		// real problem when there's git activity newer than the
		// backup. Query git's HEAD mtime once per repo.
		gitMTime := gitMTimeProvider(path)
		lagging := !gitMTime.IsZero() && gitMTime.Sub(obs.LastBackupAt) > laggingGrace

		switch {
		case age < 24*time.Hour:
			rep.Repos.FreshLast24h++
		case age < 7*24*time.Hour && lagging:
			rep.Repos.StaleOneToSeven++
		case age >= 7*24*time.Hour && lagging:
			rep.Repos.StaleOverSeven++
		case age >= 24*time.Hour && !lagging:
			// Backup is "old" by age, but git itself hasn't moved
			// since — the backup is correctly current.
			rep.Repos.Idle++
		default:
			rep.Repos.StaleOneToSeven++ // catch-all: be conservative
		}

		if lagging {
			rep.Repos.LaggingBackups = append(rep.Repos.LaggingBackups, RepoLaggingBackup{
				Path:       path,
				GitMTime:   gitMTime,
				BackupAt:   obs.LastBackupAt,
				LagSeconds: gitMTime.Sub(obs.LastBackupAt).Seconds(),
			})
		}
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i].t.Before(ages[j].t) })
	// Always include the top-5 oldest so operators can see at-a-glance
	// which repos are dragging the fleet down.
	const topN = 5
	for i, a := range ages {
		if i >= topN {
			break
		}
		rep.Repos.OldestBackup = append(rep.Repos.OldestBackup, RepoBackupAge{
			Path:  a.path,
			Since: a.t,
			AgeS:  now.Sub(a.t).Seconds(),
		})
	}
	sort.Strings(rep.Repos.NeverBackedNames)
	// Sort lagging backups worst-first so the text renderer's
	// "show top N" output points at the most-overdue repos.
	sort.Slice(rep.Repos.LaggingBackups, func(i, j int) bool {
		return rep.Repos.LaggingBackups[i].LagSeconds > rep.Repos.LaggingBackups[j].LagSeconds
	})
}

func summarisePeers(machine *config.MachineConfigV2, state *config.StateV2, now time.Time, rep *HealthReport) {
	if machine == nil {
		return
	}
	rep.Peers.Known = len(machine.Peers)

	// Three potential sources of "when was this peer last seen?":
	//
	//  1. Live Syncthing connections (best — proves the peer is
	//     reachable RIGHT NOW; timestamp is the call time).
	//  2. state.LastSeenPeers cache (good — what the daemon last
	//     observed during a prior reconcile cycle).
	//  3. Nothing (fallback — render "never seen").
	//
	// Merge in priority order: live observation wins over cache
	// because we're certain it's correct as of the call, whereas
	// the cache may be hours stale. When neither is available the
	// peer is listed as "never seen" which is the operationally
	// honest answer.
	live, _ := liveConnectionsProvider() // nil on error; safe to range over

	var cache map[string]time.Time
	if state != nil {
		cache = state.LastSeenPeers
	}

	for _, p := range machine.Peers {
		when := time.Time{}
		if t, ok := live[p.DeviceID]; ok {
			when = t
		} else if t, ok := cache[p.DeviceID]; ok {
			when = t
		}
		ageS := float64(0)
		if !when.IsZero() {
			ageS = now.Sub(when).Seconds()
		}
		rep.Peers.LastSeen = append(rep.Peers.LastSeen, PeerLastSeen{
			Name:     p.Name,
			Since:    when,
			AgeS:     ageS,
			DeviceID: p.DeviceID,
		})
	}
	sort.Slice(rep.Peers.LastSeen, func(i, j int) bool {
		return rep.Peers.LastSeen[i].Name < rep.Peers.LastSeen[j].Name
	})
}

func scanRecentActivity(now time.Time) (*RecentActivity, error) {
	logPath := filepath.Join(config.StateDir(), "syncthing.log")
	f, err := os.Open(logPath) // #nosec G304 -- fixed path under XDG_STATE_HOME
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// Tail the last logTailBytes — bounded scan keeps the command
	// snappy even with a multi-GB log. If the file is smaller,
	// scan from the start.
	offset := fi.Size() - logTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	cutoff := now.Add(-24 * time.Hour)
	act := &RecentActivity{
		WindowStart:  cutoff,
		WindowEnd:    now,
		BytesScanned: int64(len(data)),
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		// Slog text-handler format: `time=<RFC3339> level=<LEVEL> msg="..."`
		ts, ok := extractSlogTimestamp(line)
		if !ok {
			continue
		}
		if ts.Before(cutoff) {
			continue
		}
		switch {
		case strings.Contains(line, "level=ERROR"):
			act.ErrorCount++
		case strings.Contains(line, "level=WARN"):
			act.WarnCount++
		}
		if strings.Contains(line, "auto: resolve sync conflict") ||
			strings.Contains(line, `msg="resolved conflict"`) {
			act.ConflictResolved++
		}
		if strings.Contains(line, `msg="propagator: push failed"`) {
			act.PushFailures++
		}
	}
	return act, nil
}

// extractSlogTimestamp parses the `time=<RFC3339>` prefix that
// slog's TextHandler emits. Returns (zero, false) for any line
// that doesn't match — log lines occasionally come from other
// sources (Syncthing's own logger, ad-hoc fmt.Fprintln from older
// code paths) and we just skip those rather than fail the scan.
func extractSlogTimestamp(line string) (time.Time, bool) {
	const prefix = "time="
	if !strings.HasPrefix(line, prefix) {
		return time.Time{}, false
	}
	rest := line[len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end <= 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, rest[:end])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func writeHealthJSON(w io.Writer, r *HealthReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// hp wraps an io.Writer in helpers that drop the return values
// from Fprintf/Fprintln. errcheck (configured in CI) flags every
// unchecked fmt.Fprint*; routing all health-text output through
// hp keeps the body readable instead of riddled with `_, _ =`.
// Write errors on stdout/stderr are not recoverable anyway.
type hp struct{ w io.Writer }

func (p hp) Ln(s string)                          { _, _ = fmt.Fprintln(p.w, s) }
func (p hp) F(format string, args ...interface{}) { _, _ = fmt.Fprintf(p.w, format, args...) }

func writeHealthText(w io.Writer, r *HealthReport) {
	p := hp{w}
	p.Ln("=== Machine ===")
	if r.Machine.Name == "" {
		p.Ln("  Not initialised (run 'dotkeeper init')")
	} else {
		p.F("  Name: %s\n", r.Machine.Name)
		p.F("  Slot: %d\n", r.Machine.Slot)
	}

	p.F("\n=== Repos (%d tracked) ===\n", r.Repos.Total)
	p.F("  Fresh (<24h):                  %d\n", r.Repos.FreshLast24h)
	p.F("  Idle (dormant, backup OK):     %d\n", r.Repos.Idle)
	if r.Repos.StaleOneToSeven > 0 {
		p.F("  Lagging (1-7d behind git):     %d\n", r.Repos.StaleOneToSeven)
	}
	if r.Repos.StaleOverSeven > 0 {
		p.F("  Lagging (>7d behind git):      %d\n", r.Repos.StaleOverSeven)
	}
	if r.Repos.NeverBackedUp > 0 {
		p.F("  Never backed up:               %d\n", r.Repos.NeverBackedUp)
	}
	// "Lagging backups" table replaces the old "Oldest backups"
	// in the degraded case — operators want to see WHICH repos
	// are behind, not which are old. When nothing is lagging,
	// fall back to the oldest-backups list for general
	// situational awareness.
	if len(r.Repos.LaggingBackups) > 0 {
		p.Ln("  Lagging backups (git newer than backup):")
		const topN = 5
		for i, row := range r.Repos.LaggingBackups {
			if i >= topN {
				break
			}
			p.F("    %s  (lag %s)\n", row.Path, durationHuman(time.Duration(row.LagSeconds)*time.Second))
		}
	} else if len(r.Repos.OldestBackup) > 0 {
		p.Ln("  Oldest backups (informational; all current vs git):")
		for _, row := range r.Repos.OldestBackup {
			p.F("    %s  (%s ago)\n", row.Path, durationHuman(time.Duration(row.AgeS)*time.Second))
		}
	}

	p.F("\n=== Peers (%d known) ===\n", r.Peers.Known)
	for _, peer := range r.Peers.LastSeen {
		if peer.Since.IsZero() {
			p.F("  %s  (never seen)\n", peer.Name)
			continue
		}
		p.F("  %s  (last seen %s ago)\n", peer.Name, durationHuman(time.Duration(peer.AgeS)*time.Second))
	}

	if r.RecentActivity != nil {
		p.Ln("\n=== Recent activity (last 24h, log tail) ===")
		p.F("  Conflicts auto-resolved: %d\n", r.RecentActivity.ConflictResolved)
		p.F("  Push failures:           %d\n", r.RecentActivity.PushFailures)
		p.F("  Errors in log:           %d\n", r.RecentActivity.ErrorCount)
		p.F("  Warnings in log:         %d\n", r.RecentActivity.WarnCount)
	}

	if r.degraded() {
		p.Ln("\n[dotkeeper] degraded — see above")
	} else {
		p.Ln("\n[dotkeeper] healthy")
	}
}

// durationHuman formats a Duration for the at-a-glance text view.
// Shorter than time.Duration.String() for the common case
// ("3d2h" rather than "74h12m0s") and capped at days because
// hour-level precision past a week is operationally meaningless.
func durationHuman(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

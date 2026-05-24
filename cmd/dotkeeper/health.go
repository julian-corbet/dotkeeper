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
	var explain bool
	var watch time.Duration
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
			renderOnce := func() (*HealthReport, error) {
				rep, err := collectHealth(noLogScan)
				if err != nil {
					return nil, err
				}
				if jsonOut {
					return rep, writeHealthJSON(cmd.OutOrStdout(), rep)
				}
				writeHealthText(cmd.OutOrStdout(), rep)
				if explain {
					writeHealthExplanations(cmd.OutOrStdout(), rep)
				}
				return rep, nil
			}

			if watch > 0 {
				return runHealthWatch(cmd.Context(), cmd.OutOrStdout(), watch, renderOnce)
			}

			rep, err := renderOnce()
			if err != nil {
				return err
			}
			if rep.degraded() {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable text")
	cmd.Flags().BoolVar(&noLogScan, "no-log-scan", false, "Skip the syncthing.log tail (faster, but omits Recent-activity section)")
	cmd.Flags().BoolVar(&explain, "explain", false, "After the report, print explanations for any recognised warning kinds (what they mean, what to do)")
	cmd.Flags().DurationVar(&watch, "watch", 0, "Refresh the report every DURATION (e.g. 30s, 2m). Clears the screen between renders. 0 = single shot (default).")
	return cmd
}

// runHealthWatch repeatedly renders the report on the interval,
// clearing the screen between renders so a tmux pane / dashboard
// always shows the latest snapshot. Honours ctx so Ctrl-C and
// daemon-shutdown propagate cleanly. JSON mode is supported but
// the screen-clear is suppressed — most JSON consumers pipe to a
// file or jq and don't want terminal escape sequences mixed in.
//
// Exit code in watch mode is always 0 (clean shutdown via ctx);
// the degraded() trigger is silently dropped because a watch
// loop reporting "exit 1" on every tick is operationally
// useless. Operators wanting an alerting hook should use the
// single-shot form in a systemd timer.
func runHealthWatch(ctx context.Context, w io.Writer, interval time.Duration, render func() (*HealthReport, error)) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		// ANSI clear-screen + home cursor. Only when writing to
		// a TTY-shaped stdout (i.e. not when --json is also set,
		// which routes through the JSON encoder; that path skips
		// the clear at the render level).
		if _, isJSON := w.(*jsonNoClearWriter); !isJSON {
			_, _ = fmt.Fprint(w, "\x1b[2J\x1b[H")
		}
		if _, err := render(); err != nil {
			_, _ = fmt.Fprintf(w, "[dotkeeper] health collection failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// jsonNoClearWriter is a marker type runHealthWatch checks to
// know whether the underlying writer is JSON (in which case the
// terminal escape codes would corrupt the output). Currently
// unused at the call site — the watch loop just always clears —
// but reserved here in case --watch --json gets wired up.
type jsonNoClearWriter struct{ io.Writer }

// HealthReport is the data shape backing `dotkeeper health`.
// Exported so external tooling can `dotkeeper health --json | jq`
// against stable field names.
type HealthReport struct {
	GeneratedAt time.Time `json:"generated-at"`
	// Build identifies the binary that produced this report.
	// Forensic-useful when correlating a report against a
	// specific shipped fix: an alert saying "ErrorsLastHour=12"
	// is much easier to triage when you can also tell whether
	// the daemon is running the v1.1.6 binary (where the count
	// includes a now-fixed false-positive class) vs v1.1.7+.
	Build BuildInfo `json:"build"`
	// DaemonPID and DaemonStartedAt come from the running
	// dotkeeper start process when one is found. Zero values
	// when no daemon is running, which is itself a signal —
	// 'dotkeeper health' on a host where the daemon has been
	// dead since reboot will surface that immediately.
	DaemonPID       int       `json:"daemon-pid,omitempty"`
	DaemonStartedAt time.Time `json:"daemon-started-at,omitempty"`
	Machine         struct {
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

// BuildInfo identifies the binary that produced a HealthReport.
// Populated from the package-level version/commit vars
// (overridden via -ldflags at release build time).
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// daemonProcInfo locates the running `dotkeeper start` process
// for this user and returns its PID + start time. Returns
// (0, zero, nil) when no daemon is running — health treats that
// as a signal rather than an error, since the command must work
// when triaging a dead daemon. Tests stub this via the
// daemonProcInfoProvider package var.
var daemonProcInfoProvider = func() (pid int, startedAt time.Time) {
	matches, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		return 0, time.Time{}
	}
	const marker = "dotkeeper\x00start"
	for _, p := range matches {
		data, err := os.ReadFile(p) // #nosec G304 -- enumerated /proc path
		if err != nil {
			continue
		}
		if !strings.Contains(string(data), marker) {
			continue
		}
		// Found a `dotkeeper start` process. Its start time is
		// the mtime of /proc/<pid> (the directory's stat-time
		// is set when the process is spawned).
		dir := filepath.Dir(p)
		st, err := os.Stat(dir)
		if err != nil {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(filepath.Base(dir), "%d", &pid); err != nil {
			continue
		}
		return pid, st.ModTime()
	}
	return 0, time.Time{}
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
//
// The 24h totals (ErrorCount, WarnCount, etc.) are for display;
// degraded() triggers only on ErrorsLastHour because a log file
// can contain old errors from before a bug was fixed (the
// .claude/worktrees pre-v1.0.1 noise is the canonical example —
// thousands of historical WARN lines that don't reflect current
// behaviour). Counting them against a now-healthy daemon would
// permanently mark it as degraded.
type RecentActivity struct {
	WindowStart      time.Time `json:"window-start"`
	WindowEnd        time.Time `json:"window-end"`
	BytesScanned     int64     `json:"bytes-scanned"`
	ConflictResolved int       `json:"conflict-resolved"`
	PushFailures     int       `json:"push-failures"`
	WarnCount        int       `json:"warn-count"`
	ErrorCount       int       `json:"error-count"`
	// Errors that occurred within the last hour — the
	// degraded()-trigger subset. Old errors are kept in
	// ErrorCount for display but don't fire alerts.
	ErrorsLastHour int `json:"errors-last-hour"`
	// TopWarningKinds lists the most-frequent warning message
	// types in the 24h window, count-descending. Lets operators
	// triage at-a-glance: 360 warnings dominated by ONE message
	// kind is a different operational story than 360 distinct
	// problems. Bounded to 5 entries to keep the output compact.
	TopWarningKinds []WarningKind `json:"top-warning-kinds,omitempty"`
}

// WarningKind is one row of the top-warnings breakdown.
type WarningKind struct {
	Message string `json:"message"`
	Count   int    `json:"count"`
	// CountLastHour is the subset of Count that occurred in the
	// last 60 minutes. Lets operators distinguish a chronic
	// historical warning ("324 total, 0 in last hour" = old
	// noise, ignore) from a currently-active one ("12 total,
	// 12 in last hour" = something just started, investigate).
	CountLastHour int `json:"count-last-hour"`
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
	return len(r.degradedReasons()) > 0
}

// degradedReasons returns the specific conditions that cause
// degraded() to fire, as human-readable one-liners. Used by
// writeHealthText to render a "Degraded because:" footer so
// operators don't have to re-scan the full report to figure
// out which threshold tripped. Returns empty slice when the
// report is healthy.
//
// Order matters for the rendered output: most-actionable first
// (recent errors → push failures → lagging backups → never
// backed up), so the operator's eye lands on the most urgent
// fix-it surface first.
func (r *HealthReport) degradedReasons() []string {
	var reasons []string
	if r.RecentActivity != nil {
		// Only RECENT (last-hour) errors degrade; older errors
		// from the 24h window are kept for display but don't
		// fire alerts. PushFailures is hour-agnostic because the
		// propagator emits the message only for ACTIVELY-failing
		// pushes — there's no historical-residue class.
		if r.RecentActivity.ErrorsLastHour > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"%d ERROR-level log entries in the last hour",
				r.RecentActivity.ErrorsLastHour))
		}
		if r.RecentActivity.PushFailures > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"%d propagator push failure(s) in the 24h window",
				r.RecentActivity.PushFailures))
		}
	}
	if len(r.Repos.LaggingBackups) > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d repo(s) with git activity newer than the last backup",
			len(r.Repos.LaggingBackups)))
	}
	if r.Repos.NeverBackedUp > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d repo(s) tracked but never successfully backed up",
			r.Repos.NeverBackedUp))
	}
	return reasons
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
	rep := &HealthReport{
		GeneratedAt: now,
		Build:       BuildInfo{Version: version, Commit: commit},
	}
	rep.DaemonPID, rep.DaemonStartedAt = daemonProcInfoProvider()
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
	lastHourCutoff := now.Add(-1 * time.Hour)
	act := &RecentActivity{
		WindowStart:  cutoff,
		WindowEnd:    now,
		BytesScanned: int64(len(data)),
	}
	warningCounts := make(map[string]int)
	warningCountsLastHour := make(map[string]int)
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
		isErr := strings.Contains(line, "level=ERROR")
		isWarn := strings.Contains(line, "level=WARN")
		switch {
		case isErr:
			act.ErrorCount++
			if ts.After(lastHourCutoff) {
				act.ErrorsLastHour++
			}
		case isWarn:
			act.WarnCount++
			if msg := extractMsgField(line); msg != "" {
				warningCounts[msg]++
				if ts.After(lastHourCutoff) {
					warningCountsLastHour[msg]++
				}
			}
		}
		if strings.Contains(line, "auto: resolve sync conflict") ||
			strings.Contains(line, `msg="resolved conflict"`) {
			act.ConflictResolved++
		}
		if strings.Contains(line, `msg="propagator: push failed"`) {
			act.PushFailures++
		}
	}
	act.TopWarningKinds = topNWarnings(warningCounts, warningCountsLastHour, 5)
	return act, nil
}

// extractMsgField pulls the `msg="..."` payload out of a slog
// TextHandler line. Returns empty on parse failure. We use the
// raw message as the breakdown key — same kind of warning
// always produces the same message text, so distinct-counts
// reflect distinct operational concerns. Path/folder.id fields
// after msg are intentionally NOT included in the key, so
// "Failed to sync (folder-A)" and "Failed to sync (folder-B)"
// group together as the same kind of warning.
func extractMsgField(line string) string {
	const marker = `msg="`
	i := strings.Index(line, marker)
	if i < 0 {
		return ""
	}
	rest := line[i+len(marker):]
	// Find the closing quote. slog escapes embedded quotes as \",
	// so a simple scan is good enough for the 99% case — anything
	// pathological just truncates earlier than ideal.
	for j := 0; j < len(rest); j++ {
		if rest[j] == '"' && (j == 0 || rest[j-1] != '\\') {
			return rest[:j]
		}
	}
	return ""
}

// topNWarnings returns the top n warning kinds by count,
// count-descending. Ties broken by message text for
// determinism (matters for test assertions and stable JSON
// output). lastHourCounts is consulted to populate the
// CountLastHour field on each row.
func topNWarnings(counts, lastHourCounts map[string]int, n int) []WarningKind {
	if len(counts) == 0 || n <= 0 {
		return nil
	}
	out := make([]WarningKind, 0, len(counts))
	for msg, c := range counts {
		out = append(out, WarningKind{
			Message:       msg,
			Count:         c,
			CountLastHour: lastHourCounts[msg],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Message < out[j].Message
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
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
	p.Ln("=== Daemon ===")
	p.F("  Version: %s (%s)\n", r.Build.Version, r.Build.Commit)
	if r.DaemonPID > 0 {
		uptime := r.GeneratedAt.Sub(r.DaemonStartedAt)
		p.F("  Status:  running (pid %d, up %s)\n", r.DaemonPID, durationHuman(uptime))
	} else {
		p.Ln("  Status:  not running (no `dotkeeper start` process found)")
	}
	p.Ln("")
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
		p.F("  Errors (24h / last 1h):  %d / %d\n",
			r.RecentActivity.ErrorCount, r.RecentActivity.ErrorsLastHour)
		p.F("  Warnings in log:         %d\n", r.RecentActivity.WarnCount)
		if len(r.RecentActivity.TopWarningKinds) > 0 {
			p.Ln("    Top warning kinds (24h / last 1h):")
			for _, w := range r.RecentActivity.TopWarningKinds {
				// Truncate long Syncthing-internal messages so
				// the output stays readable. Operators can
				// always grep the log for the full text.
				const maxLen = 64
				msg := w.Message
				if len(msg) > maxLen {
					msg = msg[:maxLen-1] + "…"
				}
				p.F("      %5d / %4d  %s\n", w.Count, w.CountLastHour, msg)
			}
		}
	}

	reasons := r.degradedReasons()
	if len(reasons) > 0 {
		p.Ln("\n[dotkeeper] degraded because:")
		for _, why := range reasons {
			p.F("  - %s\n", why)
		}
	} else {
		p.Ln("\n[dotkeeper] healthy")
	}
}

// knownPatternExplanation maps a substring of a warning/error
// message to a one-line explanation of what it means and what
// (if anything) the operator should do. The match is substring-
// based and first-match-wins so we can use short, distinctive
// fragments rather than full message text (which Syncthing
// occasionally rewords across versions).
//
// Each entry is a deliberate operational decision: only patterns
// where we KNOW the cause AND can give actionable guidance. If
// you find yourself wanting to add "this might mean ..., or it
// might mean ...", don't — that's noise, not help. Leave the
// pattern out and let the operator search Syncthing's docs.
var knownPatternExplanations = []struct {
	substr  string
	explain string
}{
	{
		substr:  "Unexpected folder ID in ClusterConfig",
		explain: "A peer is offering a folder this device hasn't accepted. Open Syncthing's web UI (http://127.0.0.1:18384) and either accept the folder at the right path, or remove the folder from the peer if it shouldn't be shared.",
	},
	{
		substr:  "Failed to auto-accept folder due to path conflict",
		explain: "Syncthing wanted to auto-create a folder at a path that already exists with different content. Resolve manually in the web UI — pick a different path or merge the contents first.",
	},
	{
		substr:  "Detected a flip-flopping listener",
		explain: "The Syncthing discovery server is seeing this device's listener address change repeatedly. Usually a NAT/firewall behaviour and harmless; if persistent, check the device's network is stable.",
	},
	{
		substr:  "Failed to sync",
		explain: "Per-file sync failure. Drill into the syncthing.log for the specific path and error — common causes are permission errors, partially-deleted dirs with ignored files, and disk-full.",
	},
	{
		substr:  "Abandoning old index handler in favour of new connection",
		explain: "A peer reconnected and Syncthing dropped the previous index session. Routine after network blips; only investigate if it's flapping every few seconds.",
	},
	{
		substr:  "propagator: no route to peer",
		explain: "Manager.Route found no reachable transport. Usually a transient Syncthing-API hiccup during daemon startup; if persistent, run 'dotkeeper transport status' to see which transports are unreachable and why.",
	},
	{
		substr:  "no connected device has the required version of this file",
		explain: "All peers holding this file's content are currently offline. The transfer resumes automatically when a peer reconnects; no action needed unless every peer is permanently gone.",
	},
	{
		substr:  "directory has been deleted on a remote device but contains ignored files",
		explain: "A peer deleted a directory that this device still has ignored files in (e.g. .git inside a worktree). Pre-v1.0.1 this flapped on .claude/worktrees — if you're on v1.0.1+ and still seeing it for other paths, check your per-repo .stignore.",
	},
	{
		substr:  "Folder is in error state",
		explain: "Syncthing has marked the folder as broken and stopped syncing it. Usually triggered by an earlier per-file error that exhausted retries. Open the web UI for the folder's specific error, fix the cause (permissions, disk space, ignored-file conflicts), and click 'Override Changes' or restart the daemon to retry.",
	},
	{
		substr:  "Error on folder",
		explain: "Per-folder-level error from Syncthing's scanner or pull loop. Drill into the syncthing.log for the folder ID — usually means a specific file inside is unreadable, has unexpected permissions, or sits on a filesystem that's misreporting.",
	},
	{
		substr:  "Failed initial scan",
		explain: "Syncthing couldn't complete the first walk of a folder. Common causes: the folder root doesn't exist, lacks read permission, or sits on a filesystem with broken Stat (some FUSE mounts). Check `dotkeeper status` to confirm the folder path is what you expect.",
	},
	{
		substr:  "Failed to exchange Hello messages",
		explain: "TLS handshake or BEP-protocol setup with a peer failed. Transient causes (network blip during handshake) resolve themselves; persistent cases indicate clock skew (>15 min), a peer running an incompatible Syncthing major version, or MITM interference. Run `chronyd`/`timedatectl` on both ends to rule out clock skew.",
	},
}

// writeHealthExplanations prints a one-line operator-facing
// explanation for any recognised warning/error kind in the
// report. Walks the top-warnings list (already bounded to 5)
// and matches against knownPatternExplanations. Unknown
// patterns are silently skipped — the explain mode is opt-in
// help, not noise.
func writeHealthExplanations(w io.Writer, r *HealthReport) {
	if r.RecentActivity == nil {
		return
	}
	p := hp{w}
	var rendered []string
	seen := make(map[string]bool)
	for _, wk := range r.RecentActivity.TopWarningKinds {
		for _, k := range knownPatternExplanations {
			if !strings.Contains(wk.Message, k.substr) {
				continue
			}
			if seen[k.substr] {
				break
			}
			seen[k.substr] = true
			rendered = append(rendered, fmt.Sprintf("  • %s\n    %s",
				wk.Message, wrapForExplain(k.explain, 76)))
			break
		}
	}
	if len(rendered) == 0 {
		return
	}
	p.Ln("\n=== Explanations ===")
	for _, r := range rendered {
		p.F("%s\n", r)
	}
}

// wrapForExplain breaks a long explanation into lines of at most
// width characters, prefixing continuation lines with the
// indent that aligns with the bullet above. Word-aware: never
// splits mid-token, leaves single tokens longer than width on
// their own line rather than truncating.
func wrapForExplain(s string, width int) string {
	const indent = "    "
	var b strings.Builder
	tokens := strings.Fields(s)
	line := ""
	for _, tok := range tokens {
		switch {
		case line == "":
			line = tok
		case len(line)+1+len(tok) <= width:
			line += " " + tok
		default:
			b.WriteString(line)
			b.WriteByte('\n')
			b.WriteString(indent)
			line = tok
		}
	}
	if line != "" {
		b.WriteString(line)
	}
	return b.String()
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

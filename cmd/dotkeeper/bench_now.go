// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/julian-corbet/dotkeeper/internal/benchmarker"
	"github.com/julian-corbet/dotkeeper/internal/transport"
	"github.com/spf13/cobra"
)

// benchNowCmd implements `dotkeeper bench-now [--folder=PATH]`,
// the operator-on-demand counterpart to the daemon's periodic
// benchmark loop. Runs one synthetic-payload probe per
// (synchronous transport, paired peer) pair against the named
// folder (or every managed folder when no --folder flag is given)
// and prints a table of measured durations.
//
// Bypasses the cadence/quiet/convergence gates the background
// loop uses — the operator explicitly asked, so the gates would
// only suppress what they came for. Observations are still fed
// into the cost model so the manual probe also helps subsequent
// routing.
func benchNowCmd() *cobra.Command {
	var folderFlag string
	cmd := &cobra.Command{
		Use:   "bench-now",
		Short: "Measure synchronous-transport latency right now",
		Long: `Runs one 64KB probe per (synchronous transport, paired peer) pair
against every managed folder (or just --folder when set) and
prints a table of measured durations. Updates the cost model so
the daemon's subsequent routing decisions reflect the fresh
numbers.

Use this to investigate a routing decision you think is wrong, or
to measure a new peer's latency without waiting for the daemon's
24h periodic benchmark cycle.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBenchNow(cmd.Context(), folderFlag)
		},
	}
	cmd.Flags().StringVar(&folderFlag, "folder", "",
		"Restrict probing to a single folder (absolute path); default: every managed folder")
	return cmd
}

func runBenchNow(ctx context.Context, folderFlag string) error {
	mgr := startTransports(ctx, slog.Default())
	if mgr == nil {
		return fmt.Errorf("no transport manager available (Syncthing not initialised?)")
	}

	folders := liveBenchmarkerFoldersSource()()
	peers := livePeersSource()
	if folderFlag != "" {
		abs, err := filepath.Abs(folderFlag)
		if err != nil {
			return fmt.Errorf("resolve --folder: %w", err)
		}
		filtered := folders[:0]
		for _, f := range folders {
			if f.Path == abs {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("no managed folder at %s", abs)
		}
		folders = filtered
	}
	if len(folders) == 0 {
		return fmt.Errorf("no managed folders to benchmark")
	}

	// Discover so the manager knows which transports are
	// reachable for each peer; the benchmarker checks this snapshot.
	for _, p := range peers() {
		mgr.Discover(ctx, p)
	}

	b := benchmarker.New(mgr,
		func() []transport.Folder { return folders },
		peers,
		slog.Default(),
		benchmarker.Options{})

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TRANSPORT\tPEER\tFOLDER\tELAPSED\tRESULT")
	for _, folder := range folders {
		for _, r := range b.BenchmarkNow(ctx, folder) {
			result := "ok"
			if r.Err != nil {
				result = "FAIL: " + r.Err.Error()
			}
			folderLabel := folderLabelFor(r.Folder, folders)
			// Elapsed comes from the cost-model's recorded sample;
			// the BenchmarkNow path feeds RecordTransfer
			// internally, so the most-recent ms_per_byte reflects
			// the probe we just ran.
			_, msPerByte, _ := mgr.ModelParametersFor(r.Transport, r.Peer, r.Folder)
			perKBms := msPerByte * 1024
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f ms/KB\t%s\n",
				r.Transport, r.Peer, folderLabel, perKBms, result)
		}
	}
	return tw.Flush()
}

// folderLabelFor returns a short human-readable label for the
// folder ID found in r.Folder, scoped to the folders the CLI was
// invoked over. Falls back to the raw ID when no match — defensive
// against the benchmarker returning a folder we didn't pass in
// (shouldn't happen but doesn't deserve a panic).
func folderLabelFor(folderID string, folders []transport.Folder) string {
	for _, f := range folders {
		if f.ID == folderID {
			// Show the basename — readable, stable across machines
			// that mirror paths.
			return filepath.Base(f.Path)
		}
	}
	return folderID
}

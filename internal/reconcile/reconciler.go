// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
package reconcile

import (
	"context"
	"log/slog"
)

// DesiredProvider returns the current desired configuration for this machine.
// Implementations typically read machine.toml and all tracked dotkeeper.toml
// files.
type DesiredProvider func(ctx context.Context) (Desired, error)

// ObservedProvider returns the current observed state of the system by
// querying Syncthing and git.
type ObservedProvider func(ctx context.Context) (Observed, error)

// Reconciler drives a single reconcile pass: it calls DesiredProvider and
// ObservedProvider, computes a Plan via Diff, and applies each Action in
// order via Applier. Failures are logged and do not abort subsequent actions.
type Reconciler struct {
	// Desired provides the declarative configuration.
	Desired DesiredProvider

	// Observed provides the live system state.
	Observed ObservedProvider

	// Applier executes each Action produced by Diff.
	Applier Applier

	// Logger is used to log applied actions and any errors encountered.
	Logger *slog.Logger
}

// Reconcile runs a single reconcile pass. It returns the Plan that was
// computed and the first non-nil error encountered during apply, if any.
// All actions are attempted even if an earlier action fails (continue-on-error).
func (r *Reconciler) Reconcile(ctx context.Context) (Plan, error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}

	desired, err := r.Desired(ctx)
	if err != nil {
		return nil, err
	}

	observed, err := r.Observed(ctx)
	if err != nil {
		return nil, err
	}

	plan := Diff(desired, observed)

	var firstErr error
	for _, action := range plan {
		desc := action.Describe()
		logger.InfoContext(ctx, "applying action", "action", desc)
		if applyErr := r.Applier.Apply(ctx, action); applyErr != nil {
			logger.ErrorContext(ctx, "action failed", "action", desc, "err", applyErr)
			if firstErr == nil {
				firstErr = applyErr
			}
			// continue-on-error: attempt remaining actions
		}
	}

	return plan, firstErr
}

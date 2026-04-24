// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package reconcile implements the reconciler loop described in ADR 0003.
package reconcile

import "context"

// Applier executes a single Action against live system state. Implementations
// are expected to be idempotent: applying the same Action twice must not
// produce duplicate side effects.
type Applier interface {
	Apply(ctx context.Context, action Action) error
}

// StubApplier records every Action passed to Apply and can inject errors on
// demand. It is intended for use in unit tests.
type StubApplier struct {
	// Applied is the ordered list of Actions that have been applied.
	Applied []Action

	// Errors maps Action.Describe() strings to errors that should be returned
	// for that action. Keys not present result in a nil error.
	Errors map[string]error
}

// Apply records the action and returns any configured error for it.
func (s *StubApplier) Apply(_ context.Context, action Action) error {
	s.Applied = append(s.Applied, action)
	if s.Errors != nil {
		if err, ok := s.Errors[action.Describe()]; ok {
			return err
		}
	}
	return nil
}

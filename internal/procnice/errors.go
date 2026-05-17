// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package procnice

import "errors"

var (
	errStdoutAlreadySet = errors.New("procnice: Stdout already set on cmd")
	errStderrAlreadySet = errors.New("procnice: Stderr already set on cmd")
)

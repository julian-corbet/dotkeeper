// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package service

import "strings"

// contains checks if s contains sub. Shared across platform implementations.
func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

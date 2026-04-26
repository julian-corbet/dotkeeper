// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"testing"
)

// TestSanitizeTOMLKey verifies UTF-8 sanitization of TOML keys.
func TestSanitizeTOMLKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"valid-utf8-日本語", "valid-utf8-日本語"},
		{"\xe8", "_"},
		{"hello\xffworld", "hello_world"},
		{"", ""},
	}

	for _, tt := range tests {
		got := sanitizeTOMLKey(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTOMLKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestPathExpansionEdgeCases tests edge cases in ExpandPath/ContractPath.
func TestPathExpansionEdgeCases(t *testing.T) {
	tests := []struct {
		input string
		desc  string
	}{
		{"", "empty string"},
		{"~", "bare tilde"},
		{"~/", "tilde slash"},
		{"~root/something", "other user tilde"},
		{"relative", "relative path"},
		{"./relative", "dot relative"},
		{"../parent", "parent relative"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			// Must not panic
			expanded := ExpandPath(tt.input)
			_ = ContractPath(expanded)
		})
	}
}

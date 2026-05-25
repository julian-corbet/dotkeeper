// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package gitident

import (
	"errors"
	"strings"
	"testing"
)

// TestCanonicalRecognisesEveryGitURLSyntax pins the load-bearing
// invariant: every form a git client might write to .git/config
// for the SAME upstream repo must produce the SAME canonical
// string. If two operators with different URL syntaxes for the
// same repo end up with different canonicals, their subscriptions
// silently won't match — the mesh fragments invisibly.
func TestCanonicalRecognisesEveryGitURLSyntax(t *testing.T) {
	want := "github.com/julian-corbet/dotkeeper"
	cases := []string{
		// HTTPS variants (most common from `gh repo clone`)
		"https://github.com/julian-corbet/dotkeeper.git",
		"https://github.com/julian-corbet/dotkeeper",
		"https://github.com/julian-corbet/dotkeeper/",
		"HTTPS://GitHub.com/julian-corbet/dotkeeper.git",
		// HTTP (rare but seen in old configs / internal mirrors)
		"http://github.com/julian-corbet/dotkeeper.git",
		// SSH URL-form
		"ssh://git@github.com/julian-corbet/dotkeeper.git",
		"ssh://git@github.com:22/julian-corbet/dotkeeper.git", // explicit default port
		// SCP-form (the most common form for SSH cloning)
		"git@github.com:julian-corbet/dotkeeper.git",
		"git@github.com:julian-corbet/dotkeeper",
		"git@GitHub.com:julian-corbet/dotkeeper.git",
		// User-less SCP-form (rare but valid)
		"github.com:julian-corbet/dotkeeper.git",
		// git:// protocol (deprecated by GitHub but possible elsewhere)
		"git://github.com/julian-corbet/dotkeeper.git",
		// Whitespace tolerance
		"  https://github.com/julian-corbet/dotkeeper.git  ",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := Canonical(in)
			if err != nil {
				t.Fatalf("Canonical(%q) errored: %v", in, err)
			}
			if got != want {
				t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestCanonicalExplicitDefaultPortStripped pins that
// `ssh://...:22/...` collapses to host-only — different syntactic
// forms of the same remote (SCP-style with implicit port 22 vs
// URL form with explicit :22) must produce the same canonical.
// Without this, two operators who happened to type different
// forms would see subscription matches silently fail.
func TestCanonicalExplicitDefaultPortStripped(t *testing.T) {
	got, err := Canonical("ssh://git@github.com:22/foo/bar.git")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if got != "github.com/foo/bar" {
		t.Errorf("default port should be stripped; got %q", got)
	}
}

// TestCanonicalHttpsExplicitDefaultPortStripped covers the same
// rule for HTTPS — `:443` is the default and must collapse.
func TestCanonicalHttpsExplicitDefaultPortStripped(t *testing.T) {
	got, err := Canonical("https://github.com:443/foo/bar.git")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if got != "github.com/foo/bar" {
		t.Errorf("default HTTPS port should be stripped; got %q", got)
	}
}

func TestCanonicalNonDefaultPortPreserved(t *testing.T) {
	// Self-hosted Gitea on a non-default port — must keep the
	// port so the canonical is unique vs the same path on the
	// default port (if one existed).
	got, err := Canonical("ssh://git@gitea.local:2222/me/rag.git")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if got != "gitea.local:2222/me/rag" {
		t.Errorf("non-default port lost; got %q", got)
	}
}

// TestCanonicalPathCasePreserved pins that path case is preserved
// (GitHub treats `foo/Rag` and `foo/rag` as separate slugs;
// pretending they're the same would silently mis-route).
func TestCanonicalPathCasePreserved(t *testing.T) {
	got, err := Canonical("https://github.com/Foo/Rag.git")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if got != "github.com/Foo/Rag" {
		t.Errorf("path case not preserved; got %q", got)
	}
}

func TestCanonicalEmptyAndUnparseable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty string", "", ErrEmpty},
		{"whitespace only", "   \t  \n", ErrEmpty},
		{"no host", "https:///foo/bar", ErrUnparseable},
		{"scheme only", "https://", ErrUnparseable},
		{"no path on URL-form", "https://github.com", ErrUnparseable},
		{"no path on URL-form (trailing slash)", "https://github.com/", ErrUnparseable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Canonical(tc.in)
			if !errors.Is(err, tc.want) {
				t.Errorf("Canonical(%q): err = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

// TestCanonicalSelfHostedAndCustomDomains covers the non-GitHub
// case to make sure the rules don't accidentally hardcode
// GitHub-specific assumptions.
func TestCanonicalSelfHostedAndCustomDomains(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.com/group/sub/repo.git": "gitlab.com/group/sub/repo",
		"git@bitbucket.org:org/repo.git":        "bitbucket.org/org/repo",
		"https://git.corbet.ch/me/notes":        "git.corbet.ch/me/notes",
		"ssh://git@codeberg.org/foo/bar.git":    "codeberg.org/foo/bar",
		"https://gitea.local/u/r":               "gitea.local/u/r",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := Canonical(in)
			if err != nil {
				t.Fatalf("Canonical(%q): %v", in, err)
			}
			if got != want {
				t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestCanonicalNoScheme accepts an already-canonical-ish input
// (host/path with no scheme) — operators may type these directly
// in subscription configs. The output should match what the same
// repo's git URL produces.
func TestCanonicalNoScheme(t *testing.T) {
	got, err := Canonical("github.com/julian-corbet/dotkeeper")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if got != "github.com/julian-corbet/dotkeeper" {
		t.Errorf("bare host/path: got %q, want %q", got, "github.com/julian-corbet/dotkeeper")
	}
}

// TestMustCanonicalPanicsOnError pins the test-fixture contract.
func TestMustCanonicalPanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustCanonical did not panic on empty input")
		} else if !strings.Contains(r.(string), "empty") {
			t.Errorf("panic message lacks underlying error info: %q", r)
		}
	}()
	_ = MustCanonical("")
}

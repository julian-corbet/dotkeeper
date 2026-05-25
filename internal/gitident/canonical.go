// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package gitident derives stable, human-readable identity strings
// for git-backed folders from their remote URLs. The same upstream
// repository must produce the same canonical form regardless of
// which protocol or syntactic variant the operator's git client
// happened to record in `.git/config`.
//
// This is dotkeeper's load-bearing identity primitive: subscriptions
// reference repos by canonical URL; Syncthing folder labels carry
// it; the discovery surface displays it. If two machines arrive at
// different canonicals for the same repo, subscriptions won't match
// and the mesh silently fragments — so the canonicalization rules
// here are deliberately conservative and exhaustively tested.
package gitident

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrEmpty is returned by Canonical when given an empty or
// whitespace-only input. The caller should distinguish "no remote
// configured" (legitimate for non-git folders) from "unparseable
// remote" (configuration error worth surfacing).
var ErrEmpty = errors.New("gitident: empty remote URL")

// ErrUnparseable is returned when the input doesn't match any of
// the recognised git-URL syntaxes. Distinct from ErrEmpty so
// callers can react to "the operator typoed their remote" without
// a string-match.
var ErrUnparseable = errors.New("gitident: unparseable remote URL")

// scpRE matches the SCP-style remote syntax `[user@]host:path`
// (e.g. `git@github.com:foo/rag.git`). git itself treats anything
// with a colon BEFORE the first slash as SCP-form. We use a regex
// rather than ad-hoc string splitting because both the user and
// path parts may contain dots and dashes that confuse simpler
// approaches.
//
// Capture groups: 1=user (optional, may be empty), 2=host, 3=path.
var scpRE = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+)$`)

// Canonical returns the canonical identity form of a git remote
// URL. Behaviour:
//
//   - Trims surrounding whitespace.
//   - SCP-form `[user@]host:path` is rewritten to `host/path`.
//   - URL schemes (`https://`, `http://`, `ssh://`, `git://`) are
//     stripped. User-info on URL-form remotes is stripped too —
//     authentication doesn't define identity.
//   - Host is lowercased. Path case is preserved (GitHub paths are
//     case-insensitive in routing but case-sensitive on display, and
//     `foo/Rag` is the same repo as `foo/rag`; we preserve the
//     case the operator typed so the canonical matches what a
//     human reads on the GitHub page).
//   - Trailing `.git` (the conventional "this is a bare or
//     bare-style URL" suffix) is stripped.
//   - Trailing `/` is stripped.
//   - Non-default ports are preserved (`gitea.local:2222/foo/rag`).
//
// The returned form NEVER contains a scheme — it's identity, not
// a fetch URL. To reconstruct a fetchable URL, callers prepend
// `https://` or `git@`+`:` per protocol preference.
func Canonical(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ErrEmpty
	}

	// SCP-style if there's a colon before the first slash AND the
	// pre-colon portion doesn't end with `:` (which would indicate
	// it's already a `scheme://` form). The presence of `//`
	// disqualifies SCP-form: `ssh://git@host/path` is URL-form.
	if !strings.Contains(s, "://") {
		if m := scpRE.FindStringSubmatch(s); m != nil {
			host := strings.ToLower(m[2])
			path := strings.TrimPrefix(m[3], "/")
			return finish(host + "/" + path), nil
		}
	}

	// URL-form: parse via net/url after ensuring there's a scheme
	// (so url.Parse interprets `host/path` correctly).
	work := s
	if !strings.Contains(work, "://") {
		work = "https://" + work
	}
	u, err := url.Parse(work)
	if err != nil {
		return "", ErrUnparseable
	}
	host := stripDefaultPort(strings.ToLower(u.Host), u.Scheme)
	if host == "" {
		return "", ErrUnparseable
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return "", ErrUnparseable
	}
	return finish(host + "/" + path), nil
}

// stripDefaultPort removes the port from host when it matches the
// scheme's well-known default. Different syntactic forms of the
// same remote (SCP-style `git@host:path` vs URL `ssh://git@host:22/path`)
// must canonicalize identically, and the port is the only place
// they reliably differ. Non-default ports are preserved so a
// self-hosted Gitea on `:2222` keeps a distinct identity.
func stripDefaultPort(host, scheme string) string {
	colon := strings.LastIndex(host, ":")
	if colon < 0 {
		return host
	}
	port := host[colon+1:]
	defaultPort := ""
	switch strings.ToLower(scheme) {
	case "ssh":
		defaultPort = "22"
	case "https":
		defaultPort = "443"
	case "http":
		defaultPort = "80"
	case "git":
		defaultPort = "9418"
	}
	if port == defaultPort {
		return host[:colon]
	}
	return host
}

// finish applies the trailing-cleanup rules common to both
// branches above. Kept as a separate function so the two branches
// can share the rules verbatim — if they drift, identities won't
// match between operators who happened to use different URL
// syntaxes for the same repo.
func finish(s string) string {
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	return s
}

// MustCanonical is the panic-on-error variant intended for test
// fixtures and constants. Production callers should always use
// Canonical and surface ErrEmpty / ErrUnparseable to the
// operator — typos in remote URLs are exactly the class of bug
// dotkeeper exists to make visible.
func MustCanonical(raw string) string {
	c, err := Canonical(raw)
	if err != nil {
		panic("gitident.MustCanonical: " + raw + ": " + err.Error())
	}
	return c
}

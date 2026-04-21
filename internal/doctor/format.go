// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ANSI colour escape codes. Kept inline rather than pulling in a colour
// library because the set we need is tiny and the styles are stable.
const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiDim    = "\033[2m"
)

// FormatOptions controls how the text output is rendered. Zero-value
// FormatOptions produces plain ASCII without colour; WriteText populates
// a sensible default from the environment when nil is passed.
type FormatOptions struct {
	// Color enables ANSI colour escapes on the prefix symbol.
	Color bool
	// ASCII forces the ASCII prefix labels ([ok], [warn], [fail]) even
	// when the terminal could render the Unicode glyphs.
	ASCII bool
}

// DefaultFormatOptions derives sensible output options from the
// environment: colour when stdout looks like a TTY and NO_COLOR isn't
// set; Unicode glyphs unless DOTKEEPER_ASCII=1 or LANG=C requests a
// plain-ASCII fallback.
//
// The detection is deliberately conservative — we never enable colour
// or fancy glyphs without a positive signal.
func DefaultFormatOptions(isTTY bool) FormatOptions {
	opts := FormatOptions{}
	if isTTY && os.Getenv("NO_COLOR") == "" {
		opts.Color = true
	}
	if os.Getenv("DOTKEEPER_ASCII") == "1" {
		opts.ASCII = true
	}
	// LANG=C (and C.UTF-8 is notably *not* a bare "C") signals a locale
	// without multibyte support. Err on the side of ASCII.
	if lang := os.Getenv("LANG"); lang == "C" || lang == "POSIX" {
		opts.ASCII = true
	}
	return opts
}

// symbol returns the leading marker for an outcome, honouring ASCII mode
// and colour. The symbol is padded to a consistent visible width so the
// column after it lines up.
func (opts FormatOptions) symbol(o Outcome) string {
	var glyph, color string
	switch o {
	case OK:
		glyph, color = "✓", ansiGreen
		if opts.ASCII {
			glyph = "[ok]  "
		}
	case Warn:
		glyph, color = "⚠", ansiYellow
		if opts.ASCII {
			glyph = "[warn]"
		}
	case Fail:
		glyph, color = "✗", ansiRed
		if opts.ASCII {
			glyph = "[fail]"
		}
	default:
		glyph = "?"
	}
	if opts.Color {
		return color + glyph + ansiReset
	}
	return glyph
}

// nameWidth returns the column width we use for the check Name. Kept
// generous enough to fit "syncthing API" and the timer label without
// wrapping.
const nameWidth = 15

// WriteText renders the pretty, human-friendly output. When opts is nil
// it derives defaults from the environment and whether w looks like a
// TTY (best-effort: only *os.File values are probed).
func WriteText(w io.Writer, results []Result) {
	opts := DefaultFormatOptions(isTerminalWriter(w))
	writeText(w, results, opts)
}

// WriteTextWithOptions is the explicit-options variant of WriteText,
// used by tests to exercise formatting deterministically without env
// vars or TTY probing.
func WriteTextWithOptions(w io.Writer, results []Result, opts FormatOptions) {
	writeText(w, results, opts)
}

func writeText(w io.Writer, results []Result, opts FormatOptions) {
	fmt.Fprintln(w, "dotkeeper doctor")
	for _, r := range results {
		fmt.Fprintf(w, "  %s %s %s\n",
			opts.symbol(r.Outcome),
			padRight(r.Name, nameWidth),
			r.Detail,
		)
		if r.Hint != "" && r.Outcome != OK {
			// The arrow marker here is purely cosmetic — it tells the
			// eye "this line belongs to the one above" without needing
			// a blank line between entries.
			fmt.Fprintf(w, "    %s %s\n",
				strings.Repeat(" ", nameWidth-1),
				hintPrefix(opts)+r.Hint,
			)
		}
	}
	fmt.Fprintln(w)
	fails := countFailures(results)
	warns := countWarnings(results)
	switch {
	case fails == 0 && warns == 0:
		fmt.Fprintln(w, "Everything looks healthy.")
	case fails == 0:
		fmt.Fprintf(w, "Found 0 issues, %d %s.\n", warns, plural(warns, "warning", "warnings"))
	default:
		fmt.Fprintf(w, "Found %d %s, %d %s. See above.\n",
			fails, plural(fails, "issue", "issues"),
			warns, plural(warns, "warning", "warnings"),
		)
	}
}

func hintPrefix(opts FormatOptions) string {
	if opts.ASCII {
		return "-> "
	}
	if opts.Color {
		return ansiDim + "↪ " + ansiReset
	}
	return "↪ "
}

// padRight returns s right-padded with spaces to at least width runes.
// Counting runes (not bytes) matters when a check ever embeds multibyte
// text in its Name — today none do, but the format helper is cheap to
// future-proof.
func padRight(s string, width int) string {
	n := 0
	for range s {
		n++
	}
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// isTerminalWriter reports whether w is a *os.File attached to a
// terminal. Non-file writers (bytes.Buffer, pipes, etc.) always return
// false so the default output is plain text. We intentionally avoid
// importing golang.org/x/term here so the formatter stays dep-light;
// the CLI entry point can pass explicit FormatOptions when it needs
// finer control.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// WriteJSON emits the result set as a single JSON object with a results
// array. Use when scripting dotkeeper doctor or when attaching output
// to an automated issue report.
func WriteJSON(w io.Writer, results []Result) {
	payload := struct {
		Results  []jsonResult `json:"results"`
		Failures int          `json:"failures"`
		Warnings int          `json:"warnings"`
	}{
		Results:  toJSONResults(results),
		Failures: countFailures(results),
		Warnings: countWarnings(results),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// jsonResult is the on-the-wire shape of a Result. Outcome is rendered
// as the string label ("ok"/"warn"/"fail") rather than the numeric enum
// so the JSON survives enum reordering.
type jsonResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint,omitempty"`
}

func toJSONResults(results []Result) []jsonResult {
	out := make([]jsonResult, len(results))
	for i, r := range results {
		out[i] = jsonResult{
			Name:    r.Name,
			Outcome: r.Outcome.String(),
			Detail:  r.Detail,
			Hint:    r.Hint,
		}
	}
	return out
}

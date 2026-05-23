// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolverNeverRevertsHistoricalContent is a property-style
// pin on the single invariant the v1.0.2 conflict-resolver
// safeguard exists to enforce: NO auto-resolved commit may
// contain content that already exists at an EARLIER commit in
// the file's history.
//
// The stale-peer-revert pattern this catches:
//
//   1. Local repo lands content X (commit C2) via a normal git
//      operation. HEAD is now C2.
//   2. A peer that's still at content Y (commit C1, earlier) comes
//      online and Syncthing surfaces Y as a sync-conflict file.
//   3. The resolver runs. If it merges Y into HEAD (which has X
//      content), the resulting auto-commit's blob is Y again —
//      which already exists at C1. THAT'S THE BUG.
//
// The safeguard (refuse the merge when `ours == base`) means: if
// the local file matches HEAD's blob, no merge happens, no
// revert commit is produced. The invariant holds.
//
// Property test approach: generate N random {historical-content,
// conflict-content} pairs, set up the repo so the local file
// matches HEAD's historical-content, run the resolver, and
// assert that the resulting HEAD blob isn't equal to ANY
// previous commit's blob for the file.
//
// We use math/rand with a fixed seed for reproducibility; a real
// failure surfaces deterministically on rerun, which is what you
// want when triaging a regression.
func TestResolverNeverRevertsHistoricalContent(t *testing.T) {
	const N = 30
	rng := rand.New(rand.NewSource(0xd07ee9e2)) //nolint:gosec // deterministic test inputs (seed: "dotke9e2")

	for i := 0; i < N; i++ {
		i := i
		t.Run("iter", func(t *testing.T) {
			repo := gitInit(t, t.TempDir())

			// Generate a small history of commits with random
			// content. The current HEAD's blob is the "ours"
			// content the safeguard should protect.
			const histLen = 3
			contents := make([]string, histLen)
			for h := 0; h < histLen; h++ {
				contents[h] = randomContent(rng, 32+rng.Intn(256))
				gitCommit(t, repo, "doc.txt", contents[h], "commit "+string(rune('a'+h)))
			}
			headContent := contents[histLen-1]

			// Conflict content: pick a RANDOM prior commit's
			// content. This is exactly the stale-peer pattern.
			theirsContent := contents[rng.Intn(histLen-1)]

			c := makeConflict(t, repo, "doc.txt")
			if err := os.WriteFile(c.Path, []byte(theirsContent), 0o644); err != nil {
				t.Fatal(err)
			}

			// Sanity: local matches HEAD content (no local edits).
			localBytes, err := os.ReadFile(filepath.Join(repo, "doc.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(localBytes) != headContent {
				t.Fatalf("setup bug: local != HEAD content (%d)", i)
			}

			action, err := ResolveTextMerge(testCtx(t), c, repo)
			if err != nil {
				// Errors are acceptable (the resolver may refuse
				// for any reason). What's forbidden is a
				// successful merge that lands a revert commit.
				t.Logf("iter %d: ResolveTextMerge error (acceptable): %v", i, err)
				return
			}

			// The invariant: if the resolver claims ActionMerged,
			// the new HEAD blob must NOT equal any previous blob
			// for this path.
			if action == ActionMerged {
				blobs := historicalBlobs(t, repo, "doc.txt")
				if len(blobs) < 2 {
					t.Fatalf("iter %d: expected ≥2 historical blobs, got %d", i, len(blobs))
				}
				current := blobs[0]
				priors := blobs[1:]
				for _, prior := range priors {
					if current == prior {
						t.Errorf("iter %d: SAFEGUARD VIOLATED — merged HEAD blob %s exists in prior history",
							i, current[:12])
						break
					}
				}
			}

			// Safeguard expected behaviour under "ours==base":
			// ActionKeep, no commit, conflict file preserved.
			if action != ActionKeep {
				t.Errorf("iter %d: expected ActionKeep (ours==base), got %q", i, action)
			}
		})
	}
}

func randomContent(rng *rand.Rand, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

// historicalBlobs returns the hex-encoded SHA256 of `relPath`'s
// content at every commit in HEAD's first-parent line, newest
// first. We hash the content ourselves (rather than asking git
// for its blob OID) because that's portable across git versions
// without depending on git's object-database format.
func historicalBlobs(t *testing.T, repo, relPath string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "log", "--pretty=format:%H", "--", relPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	var hashes []string
	for _, sha := range strings.Fields(string(out)) {
		show := exec.Command("git", "-C", repo, "show", sha+":"+relPath)
		body, err := show.Output()
		if err != nil {
			continue
		}
		sum := sha256.Sum256(body)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	return hashes
}

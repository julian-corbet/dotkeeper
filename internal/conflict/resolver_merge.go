// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package conflict

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// textDetectionWindow is the number of leading bytes scanned for NUL to
// classify a file as text vs binary. Matches what git diff uses (8000
// bytes) closely enough while staying a nice round number.
const textDetectionWindow = 8 * 1024

// isTextFile returns true when the first textDetectionWindow bytes of
// path contain no NUL bytes. Returns false for any read error so callers
// can't accidentally treat an unreadable blob as safe to merge.
func isTextFile(path string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- path is a scanner-produced absolute path
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, textDetectionWindow)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return !bytes.Contains(buf[:n], []byte{0}), nil
}

// gitShowHEAD returns the contents of relPath at HEAD in the given repo.
// A missing-file result (exit code) is mapped to (nil, os.ErrNotExist)
// so callers can cleanly distinguish "no common ancestor" from "git
// broken".
func gitShowHEAD(ctx context.Context, repoRoot, relPath string) ([]byte, error) {
	// "HEAD:<path>" is git's pathspec for a blob at a commit.
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "show", "HEAD:"+relPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}

	// Any non-zero exit on `git show` means "path not in HEAD" in
	// practice — a broken repo throws a different, usually worded
	// differently error. Treat as not-exist and let the caller escalate.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("%w: git show HEAD:%s: %s",
			os.ErrNotExist, relPath, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil, fmt.Errorf("git show HEAD:%s: %w", relPath, err)
}

// writeTempFile writes data to a fresh file in dir and returns its path.
// Used to stage the three inputs for git-merge-file. The name prefix is
// recorded so debuggers can tell which side it was when triaging.
func writeTempFile(dir, prefix string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, prefix+"-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// ResolveTextMerge attempts a 3-way git-merge-file resolution. The
// algorithm is:
//  1. If either file is binary (NUL in first 8KB), return ActionKeep.
//  2. Read the ancestor from HEAD via `git show`. If the path isn't in
//     HEAD (e.g. added but never committed), return ActionKeep — we
//     can't merge without a base.
//  3. Stage ancestor / local / conflict in a temp dir and run
//     `git merge-file --stdout ours base theirs`.
//  4. Exit 0: clean merge. Write the result back to the local file,
//     delete the conflict, stage + commit the local file.
//  5. Exit 1..127: conflict markers; leave both files in place.
//  6. Exit >= 128: tooling failure; return the error.
//
// repoRoot is the absolute path of the git repo that contains the local
// file. The caller is responsible for passing the correct root —
// typically the managed-folder root.
func ResolveTextMerge(ctx context.Context, c Conflict, repoRoot string) (Action, error) {
	local := localFilePath(c)

	// 1. Binary detection. Check both sides: a file can flip from text
	// to binary between commits, and we don't want to corrupt either.
	localText, err := isTextFile(local)
	if err != nil {
		return ActionKeep, fmt.Errorf("classify local %s: %w", local, err)
	}
	if !localText {
		return ActionKeep, nil
	}
	conflictText, err := isTextFile(c.Path)
	if err != nil {
		return ActionKeep, fmt.Errorf("classify conflict %s: %w", c.Path, err)
	}
	if !conflictText {
		return ActionKeep, nil
	}

	// 2. Need a path relative to repoRoot for both git-show and the
	// eventual `git add`. Reject paths outside the repo as a safety net.
	relPath, err := filepath.Rel(repoRoot, local)
	if err != nil {
		return ActionKeep, fmt.Errorf("rel path for %s under %s: %w", local, repoRoot, err)
	}
	if relPath == ".." || (len(relPath) >= 3 && relPath[:3] == ".."+string(filepath.Separator)) {
		return ActionKeep, fmt.Errorf("local path %s is outside repoRoot %s", local, repoRoot)
	}
	// git expects forward slashes on all platforms.
	relPathGit := filepath.ToSlash(relPath)

	ancestor, err := gitShowHEAD(ctx, repoRoot, relPathGit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File not tracked yet — no base, no merge.
			return ActionKeep, nil
		}
		return ActionKeep, err
	}

	// 3. Stage the three inputs. Local is "ours", conflict is "theirs".
	localData, err := os.ReadFile(local) // #nosec G304 -- absolute scanner path
	if err != nil {
		return ActionKeep, fmt.Errorf("read local %s: %w", local, err)
	}
	conflictData, err := os.ReadFile(c.Path) // #nosec G304 -- absolute scanner path
	if err != nil {
		return ActionKeep, fmt.Errorf("read conflict %s: %w", c.Path, err)
	}

	tmpDir, err := os.MkdirTemp("", "dotkeeper-merge-")
	if err != nil {
		return ActionKeep, fmt.Errorf("mkdir temp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	oursPath, err := writeTempFile(tmpDir, "ours", localData)
	if err != nil {
		return ActionKeep, err
	}
	basePath, err := writeTempFile(tmpDir, "base", ancestor)
	if err != nil {
		return ActionKeep, err
	}
	theirsPath, err := writeTempFile(tmpDir, "theirs", conflictData)
	if err != nil {
		return ActionKeep, err
	}

	// 4. Run the merge. Argument order for `git merge-file` is
	//    current/base/other, i.e. (ours, base, theirs). --stdout keeps
	//    us from mutating oursPath in place if the merge succeeds, so
	//    the temp dir is cleanly disposable on every exit path.
	mergeCmd := exec.CommandContext(ctx, "git", "merge-file", "--stdout", oursPath, basePath, theirsPath)
	var stdout, stderr bytes.Buffer
	mergeCmd.Stdout = &stdout
	mergeCmd.Stderr = &stderr
	runErr := mergeCmd.Run()

	// git merge-file exit code:
	//  0      — clean
	//  1..126 — this many conflicts remain in the output
	//  127    — (reserved by shell, unlikely here)
	//  128+   — tooling error
	if runErr == nil {
		return applyCleanMerge(ctx, c, local, relPathGit, repoRoot, stdout.Bytes())
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		if code > 0 && code < 128 {
			// Conflict markers in the output — we could write them, but
			// that replaces the clean user-visible file with marker gunk.
			// Spec says: leave both in place and escalate.
			return ActionKeep, nil
		}
		return ActionKeep, fmt.Errorf("git merge-file failed (exit %d): %s",
			code, bytes.TrimSpace(stderr.Bytes()))
	}
	return ActionKeep, fmt.Errorf("git merge-file: %w", runErr)
}

// applyCleanMerge overwrites the local file with merged content, deletes
// the conflict file, and creates a scoped auto-commit. Kept separate
// from ResolveTextMerge so the hot path stays readable.
func applyCleanMerge(ctx context.Context, c Conflict, local, relPathGit, repoRoot string, merged []byte) (Action, error) {
	// Preserve the local file's mode when we overwrite it. Losing +x on
	// a merged script would be a subtle but ugly regression.
	info, err := os.Stat(local)
	if err != nil {
		return ActionKeep, fmt.Errorf("stat local %s before write: %w", local, err)
	}
	if err := writeAtomic(local, merged, info.Mode().Perm()); err != nil {
		return ActionKeep, fmt.Errorf("write merged %s: %w", local, err)
	}
	if err := os.Remove(c.Path); err != nil {
		return ActionKeep, fmt.Errorf("remove conflict %s: %w", c.Path, err)
	}

	// Stage and commit ONLY the merged file. Use `-- <path>` to prevent
	// pathspec ambiguity; `--only <path>` scopes the commit even if
	// other files are already staged in the index.
	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", "--", relPathGit)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return ActionKeep, fmt.Errorf("git add %s: %w: %s", relPathGit, err, bytes.TrimSpace(out))
	}
	// -m must come BEFORE the `--` pathspec terminator; otherwise git
	// reads "-m" as a path and dies with "pathspec did not match".
	msg := fmt.Sprintf("auto: resolve sync conflict in %s", relPathGit)
	commitCmd := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"commit", "--only", "-m", msg, "--", relPathGit)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return ActionKeep, fmt.Errorf("git commit %s: %w: %s", relPathGit, err, bytes.TrimSpace(out))
	}
	return ActionMerged, nil
}

// writeAtomic writes data to dst via a same-directory tempfile + rename.
// On POSIX this is atomic, so a crash mid-write cannot leave the local
// file half-merged — either old or new, never torn.
func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, ".dotkeeper-merge-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Best-effort cleanup if we bail out before the rename.
	defer func() {
		if _, statErr := os.Stat(tmp); statErr == nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

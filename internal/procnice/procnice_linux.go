// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package procnice lowers the CPU and I/O priority of child processes
// (primarily git invocations) so dotkeeper never crowds out user work,
// even when the daemon's own cgroup limits are not in effect (e.g. when
// the user runs dotkeeper outside of systemd).
//
// Mechanism: every spawned child is funnelled through the standard
// `nice` (coreutils) and `ionice` (util-linux) wrappers when they are
// available on PATH, so the priority is established by the wrapper
// *before* exec(2) replaces it with the real binary. This is race-free
// — there is no window between the child reaching its entry point and
// the parent calling Setpriority. When neither wrapper is on PATH the
// implementation falls back to a post-Start() setpriority/ioprio_set
// pair (best-effort: real git work takes long enough that the post-
// hoc adjustment lands well before the child does any heavy lifting).
//
// On non-Linux platforms the package is a no-op pass-through; cgroup
// or platform-specific mechanisms are expected to do the de-prio work.
package procnice

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// ioprio_set syscall constants (linux/include/uapi/linux/ioprio.h).
const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// Resolved wrapper paths are cached for the lifetime of the process:
// PATH does not change between reconciles, and a missing binary
// shouldn't appear at runtime. exec.LookPath does a stat per directory
// entry on PATH, which is cheap but not free.
var (
	wrapperOnce     sync.Once
	cachedNicePath  string
	cachedIonicePat string
)

func resolveWrappers() (nicePath, ionicePath string) {
	wrapperOnce.Do(func() {
		if p, err := exec.LookPath("nice"); err == nil {
			cachedNicePath = p
		}
		if p, err := exec.LookPath("ionice"); err == nil {
			cachedIonicePat = p
		}
	})
	return cachedNicePath, cachedIonicePat
}

// wrap prepends `nice -n 19` and (if available) `ionice -c 3` to the
// given command, mutating cmd.Path and cmd.Args in place. Returns true
// if at least one wrapper was applied (so the caller knows whether the
// post-Start() syscall fallback is still needed).
func wrap(cmd *exec.Cmd) bool {
	nicePath, ionicePath := resolveWrappers()
	if nicePath == "" && ionicePath == "" {
		return false
	}

	orig := cmd.Args
	if len(orig) == 0 {
		// Defensive: exec.Command always sets Args, but if a caller has
		// constructed an exec.Cmd by hand they may have left Args nil.
		orig = []string{cmd.Path}
	}

	// GNU `nice -n N` is a *relative* adjustment to the parent's niceness,
	// silently clamped to the legal range [-20, 19]. Using 40 (any value
	// > 39 would do) guarantees the child lands at 19 regardless of the
	// parent's starting niceness — important when the daemon itself runs
	// under a non-zero `Nice=` in its systemd unit. ionice's `-c 3` (idle
	// class) is already absolute, so no equivalent treatment is needed
	// for I/O priority.
	var newArgs []string
	var newPath string
	if nicePath != "" {
		newPath = nicePath
		newArgs = []string{"nice", "-n", "40"}
		if ionicePath != "" {
			newArgs = append(newArgs, ionicePath, "-c", "3")
		}
	} else {
		// ionice on its own. ionice with no `-p` exec-replaces with its
		// trailing argv after setting class, mirroring `nice`'s contract.
		newPath = ionicePath
		newArgs = []string{"ionice", "-c", "3"}
	}
	newArgs = append(newArgs, orig...)

	cmd.Path = newPath
	cmd.Args = newArgs
	return true
}

// lower drops the named process's CPU and I/O priority via direct
// syscalls. Best-effort fallback when the wrapper binaries aren't on
// PATH; both errors are silently swallowed because losing the niceness
// is strictly less bad than aborting the operation entirely.
func lower(pid int) {
	_ = unix.Setpriority(unix.PRIO_PROCESS, pid, 19)
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET,
		uintptr(ioprioWhoProcess),
		uintptr(pid),
		uintptr(ioprioClassIdle<<ioprioClassShift))
}

// LowerSelf drops the calling process's CPU and I/O priority to
// nice=19 / idle I/O class. Defensive for installs that don't run
// dotkeeper under a hardened systemd unit — Docker, manual
// `dotkeeper start`, third-party packagers that omitted the Nice= and
// IOSchedulingClass= directives. The packaged systemd user unit
// (Nice=19, IOSchedulingClass=idle, CPUWeight=10) already enforces
// the same values, so on those installs this is an idempotent no-op.
//
// On Linux, setpriority(2) and ioprio_set(2) are per-thread despite
// the PRIO_PROCESS name (see man 2 setpriority's NOTES on NPTL). A
// naive setpriority(PRIO_PROCESS, 0, 19) only nices the calling
// thread — fine if called before any goroutine spawns an OS thread,
// but Go's runtime has already created GOMAXPROCS threads by main()'s
// entry. We therefore enumerate /proc/self/task and lower each TID.
// Threads created *after* this call inherit their creator's nice
// value, so as long as the workers spawned by the Syncthing engine
// start from an already-niced thread, the inheritance is correct.
func LowerSelf() {
	lower(0)

	// Two passes. The Go runtime occasionally spawns a new OS thread
	// (GC worker, sysmon, scavenger) between our ReadDir and the
	// per-thread lower() call — the runtime's thread-creation site
	// is on whichever M happens to need an extra thread, which is
	// not guaranteed to be one we've already niced. A second pass
	// catches any thread that appeared during the first; threads
	// created during the second pass are vanishingly unlikely to
	// stay alive long enough to matter, and the worst case is one
	// short-lived runtime helper at the original niceness.
	for pass := 0; pass < 2; pass++ {
		entries, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return // best-effort
		}
		for _, e := range entries {
			tid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			lower(tid)
		}
	}
}

// Run starts cmd with lowered CPU and I/O priority and waits for it to
// finish. Drop-in replacement for cmd.Run().
func Run(cmd *exec.Cmd) error {
	wrapped := wrap(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if !wrapped {
		lower(cmd.Process.Pid)
	}
	return cmd.Wait()
}

// Output is a drop-in replacement for cmd.Output(). Captures stdout
// and returns it. If cmd.Stdout is already set, returns an error
// matching exec.Cmd.Output's contract.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, &exec.Error{Name: cmd.Path, Err: errStdoutAlreadySet}
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var captureErr bytes.Buffer
	if cmd.Stderr == nil {
		cmd.Stderr = &captureErr
	}
	err := Run(cmd)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) == 0 {
			exitErr.Stderr = captureErr.Bytes()
		}
	}
	return stdout.Bytes(), err
}

// CombinedOutput is a drop-in replacement for cmd.CombinedOutput().
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, &exec.Error{Name: cmd.Path, Err: errStdoutAlreadySet}
	}
	if cmd.Stderr != nil {
		return nil, &exec.Error{Name: cmd.Path, Err: errStderrAlreadySet}
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := Run(cmd)
	return buf.Bytes(), err
}

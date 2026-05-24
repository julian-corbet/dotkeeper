// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func pprofTestLogger() *slog.Logger {
	// Level higher than any builtin so the listener's INFO logs don't
	// flood `go test -v` output. Use the noopPropWriter shared with
	// other cmd/dotkeeper tests.
	return slog.New(slog.NewTextHandler(noopPropWriter{}, &slog.HandlerOptions{
		Level: slog.LevelError + 10,
	}))
}

// TestPprofListenerEmptyAddrIsNoOp documents the off-by-default
// contract: an empty PprofAddress means "do not bind anything." The
// function must not crash, must not log a bind error, and must not
// hold any resources.
func TestPprofListenerEmptyAddrIsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprofListener(ctx, "", pprofTestLogger())
	// Nothing observable to assert except that we got here without
	// panicking. The real proof is that no goroutine leaks; the test
	// framework's leak detector would catch a stray Serve loop.
}

// TestPprofListenerServesProfile binds to an OS-assigned loopback
// port, fetches /debug/pprof/, and verifies the standard index page
// is served. Proves the import of net/http/pprof actually wired the
// handlers onto DefaultServeMux and that our Serve call is using
// that mux.
func TestPprofListenerServesProfile(t *testing.T) {
	// Pick a free port by binding to :0 first, then handing the
	// resolved address to startPprofListener (which re-binds — but
	// the kernel's ephemeral-port allocator makes a second :0 land
	// somewhere usable in practice, and using a known-free port
	// avoids the test depending on a hardcoded number).
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprofListener(ctx, addr, pprofTestLogger())

	// Poll briefly for the listener to come up. startPprofListener
	// is non-blocking and the goroutine may not have entered Serve
	// before the test moves on.
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Types of profiles available") {
		t.Errorf("body did not look like pprof index page; got first 200 bytes: %q",
			truncate(string(body), 200))
	}
}

// TestPprofListenerShutsDownOnCtxCancel verifies that the goroutine
// holding the listener exits when the parent context is cancelled,
// matching the daemon's normal shutdown path. Without this, a leaked
// listener would prevent the next test run from binding the same
// port and would also keep a goroutine alive past daemon shutdown.
func TestPprofListenerShutsDownOnCtxCancel(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	startPprofListener(ctx, addr, pprofTestLogger())

	// Confirm the listener is up before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()

	// Within shutdownTimeout + a small margin, the port must be
	// reusable. Loop on Listen rather than time.Sleep so a fast
	// shutdown completes the test fast.
	deadline = time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		l, err := net.Listen("tcp", addr)
		if err == nil {
			_ = l.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("port %s still bound after 7s of cancellation; listener leaked", addr)
}

// TestPprofListenerBindFailureIsNonFatal proves the daemon doesn't
// die when pprof_address is set to an already-bound port.
// Observability surfaces must never tank the daemon.
func TestPprofListenerBindFailureIsNonFatal(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holder listen: %v", err)
	}
	defer func() { _ = holder.Close() }()
	addr := holder.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Must return without panicking even though the port is taken.
	startPprofListener(ctx, addr, pprofTestLogger())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	// pprof attaches its handlers to http.DefaultServeMux as an
	// import side effect — this is the documented way to use it.
	_ "net/http/pprof"
	"runtime"
	"time"
)

// startPprofListener binds an HTTP server on addr that serves the Go
// runtime's /debug/pprof/* endpoints. Off-by-default and
// loopback-only by convention: the caller is responsible for passing
// a 127.0.0.1:* address.
//
// Mutex and block profiling are explicitly enabled so the operator
// can capture lock contention without redeploying. Sample rates
// chosen for low steady-state overhead — 1 ms block threshold, 100%
// mutex sampling — same defaults the runtime offers for
// SetMutexProfileFraction(1) / SetBlockProfileRate(1_000_000).
//
// The listener does NOT block daemon startup: bind failures log a
// WARN and return, leaving the rest of the daemon running normally.
// Profiling endpoints are observability, not load-bearing.
//
// On ctx.Done the listener shuts down via the standard
// http.Server.Shutdown path with a 5 s drain — long enough for an
// in-flight `go tool pprof http://...` capture to finish but short
// enough that daemon shutdown isn't delayed past operator
// expectation.
func startPprofListener(ctx context.Context, addr string, logger *slog.Logger) {
	if addr == "" {
		return
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.WarnContext(ctx, "pprof: bind failed",
			"addr", addr, "err", err)
		return
	}

	// Enabling mutex and block sampling at startup lets the operator
	// capture contention immediately rather than poking at
	// SetMutexProfileFraction via a separate handler. The rates are
	// the lightest non-zero settings — block sampling at every
	// blocking event >1 ms, mutex sampling at full fidelity.
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1_000_000)

	srv := &http.Server{
		// ReadHeaderTimeout guards against slowloris on this loopback
		// listener. Profile captures are POST-less GETs, so a
		// generous body-read timeout isn't needed.
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.InfoContext(ctx, "pprof listener started",
		"addr", ln.Addr().String(),
		"hint", "go tool pprof http://"+ln.Addr().String()+"/debug/pprof/profile?seconds=30")

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.WarnContext(ctx, "pprof: serve exited",
				"err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

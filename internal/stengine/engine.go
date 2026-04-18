// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package stengine manages the embedded Syncthing lifecycle.
package stengine

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/syncthing/syncthing/lib/build"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/locations"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/svcutil"
	stlib "github.com/syncthing/syncthing/lib/syncthing"

	"github.com/syncthing/syncthing/lib/logger"

	"github.com/julian-corbet/dotkeeper/internal/stclient"

	suture "github.com/thejerf/suture/v4"
)

const (
	GUIAddress   = stclient.APIAddress // 127.0.0.1:18384
	ListenTCP    = "tcp://:12000"
	ListenQUIC   = "quic://:12000"
	LocalAnnPort = 11027
)

// stateDir returns ~/.local/state/dotkeeper/ (XDG_STATE_HOME/dotkeeper).
func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dotkeeper")
}

// redirectSyncthingLogs routes Syncthing's stdout-bound log output to
// ~/.local/state/dotkeeper/syncthing.log. Returns an *os.File wrapping the
// original stdout so dotkeeper's own output can still reach the user.
//
// Syncthing's logger captures os.Stdout at package init and offers no
// SetOutput hook. We therefore dup the real fd 1 (to preserve dotkeeper's
// stdout) and dup2 the log file onto fd 1, so anything the captured
// os.Stdout *os.File writes lands in the log file instead.
func redirectSyncthingLogs() (*os.File, error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	logPath := filepath.Join(dir, "syncthing.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening syncthing.log: %w", err)
	}

	// Save the original fd 1 so dotkeeper's own output still goes to
	// the real stdout (systemd journal, user terminal).
	origFD, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("dup stdout: %w", err)
	}

	// Point fd 1 at the log file. Syncthing's captured *os.File for stdout
	// still writes via fd 1, so its output now lands in the log file.
	if err := syscall.Dup2(int(logFile.Fd()), int(os.Stdout.Fd())); err != nil {
		logFile.Close()
		syscall.Close(origFD)
		return nil, fmt.Errorf("dup2 stdout: %w", err)
	}
	// The log file's fd is no longer needed — fd 1 is the duplicate.
	logFile.Close()

	return os.NewFile(uintptr(origFD), "dotkeeper-stdout"), nil
}

// Engine manages the embedded Syncthing instance.
type Engine struct {
	configDir string
	dataDir   string
	version   string
	app       *stlib.App
}

// New creates an engine with the given config and data directories.
// The version string (e.g. "0.1.1") is injected into Syncthing's build.Version
// so the BEP handshake reports dotkeeper's version rather than "unknown-dev".
func New(configDir, dataDir, version string) *Engine {
	return &Engine{
		configDir: configDir,
		dataDir:   dataDir,
		version:   version,
	}
}

// Setup generates the initial Syncthing configuration if it doesn't exist.
func (e *Engine) Setup() error {
	if err := os.MkdirAll(e.configDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(e.dataDir, 0o700); err != nil {
		return err
	}

	// Set Syncthing's locations to our isolated paths
	locations.SetBaseDir(locations.ConfigBaseDir, e.configDir)
	locations.SetBaseDir(locations.DataBaseDir, e.dataDir)

	configFile := locations.Get(locations.ConfigFile)
	certFile := locations.Get(locations.CertFile)
	keyFile := locations.Get(locations.KeyFile)

	// Generate certificate if needed
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		cert, err := stlib.LoadOrGenerateCertificate(certFile, keyFile)
		if err != nil {
			return fmt.Errorf("generating certificate: %w", err)
		}
		_ = cert
	}

	// Generate config if needed
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		if err := e.generateConfig(configFile, certFile, keyFile); err != nil {
			return fmt.Errorf("generating config: %w", err)
		}
	}

	return nil
}

// Start launches the embedded Syncthing instance in the foreground.
// Blocks until stopped via context cancellation or signal.
func (e *Engine) Start(ctx context.Context) error {
	locations.SetBaseDir(locations.ConfigBaseDir, e.configDir)
	locations.SetBaseDir(locations.DataBaseDir, e.dataDir)

	certFile := locations.Get(locations.CertFile)
	keyFile := locations.Get(locations.KeyFile)
	configFile := locations.Get(locations.ConfigFile)
	dbFile := locations.Get(locations.Database)

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("loading certificate: %w", err)
	}

	// Override Syncthing's build.Version so the BEP handshake reports
	// dotkeeper's version. ClientName remains "syncthing" — it is a
	// string literal in lib/connections/service.go and has no public hook.
	if e.version != "" {
		build.Version = "v" + e.version
	}

	// Route Syncthing's stdout-bound log output to a file. Must happen
	// before any syncthing code runs (stlib.LoadConfigAtStartup, etc.).
	ourStdout, err := redirectSyncthingLogs()
	if err != nil {
		// Non-fatal: fall through and let syncthing log to wherever.
		fmt.Fprintln(os.Stderr, "[dotkeeper] WARNING: redirecting syncthing logs:", err)
		ourStdout = os.Stdout
	}

	// Strip date/time prefix from syncthing log lines — systemd/journal
	// add their own timestamps, and the log file is append-only.
	logger.DefaultLogger.SetFlags(0)

	evLogger := events.NewLogger()
	spec := svcutil.SpecWithDebugLogger(logger.DefaultLogger)
	earlySvc := suture.New("early", spec)
	earlyCtx, earlyCancel := context.WithCancel(ctx)
	defer earlyCancel()
	earlySvc.ServeBackground(earlyCtx)
	earlySvc.Add(evLogger)

	cfgWrapper, err := stlib.LoadConfigAtStartup(configFile, cert, evLogger, false, true, false)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	earlySvc.Add(cfgWrapper)

	ldb, err := stlib.OpenDBBackend(dbFile, cfgWrapper.Options().DatabaseTuning)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}

	opts := stlib.Options{
		NoUpgrade: true,
	}

	app, err := stlib.New(cfgWrapper, ldb, evLogger, cert, opts)
	if err != nil {
		return fmt.Errorf("creating syncthing app: %w", err)
	}
	e.app = app

	if err := app.Start(); err != nil {
		return fmt.Errorf("starting syncthing: %w", err)
	}

	fmt.Fprintln(ourStdout, "[dotkeeper] embedded Syncthing started on", GUIAddress)

	// Wait for context cancellation or app exit
	go func() {
		<-ctx.Done()
		app.Stop(svcutil.ExitSuccess)
	}()

	status := app.Wait()
	if status != svcutil.ExitSuccess {
		return fmt.Errorf("syncthing exited with status %d", status)
	}
	return nil
}

// DeviceID returns this instance's device ID by reading the certificate.
func (e *Engine) DeviceID() (string, error) {
	certFile := filepath.Join(e.configDir, "cert.pem")
	keyFile := filepath.Join(e.configDir, "key.pem")
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return "", err
	}
	deviceID := protocol.NewDeviceID(cert.Certificate[0])
	return deviceID.String(), nil
}

// APIKey reads the API key from the Syncthing config.xml.
func (e *Engine) APIKey() (string, error) {
	configFile := filepath.Join(e.configDir, "config.xml")
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", err
	}
	var cfg xmlConfig
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	return cfg.GUI.APIKey, nil
}

// generateConfig creates a default Syncthing config with our custom settings.
func (e *Engine) generateConfig(configFile, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	deviceID := protocol.NewDeviceID(cert.Certificate[0])

	myDevice := config.DeviceConfiguration{
		DeviceID:  deviceID,
		Name:      "dotkeeper",
		Addresses: []string{"dynamic"},
	}

	cfg := config.New(deviceID)
	cfg.Devices = []config.DeviceConfiguration{myDevice}
	cfg.Folders = nil // no default folder

	// GUI / API
	cfg.GUI.RawAddress = GUIAddress
	cfg.GUI.Enabled = true

	// Listen addresses
	cfg.Options.RawListenAddresses = []string{ListenTCP, ListenQUIC}

	// Discovery + connectivity — use Syncthing's full network stack
	cfg.Options.LocalAnnEnabled = true
	cfg.Options.LocalAnnPort = LocalAnnPort
	cfg.Options.GlobalAnnEnabled = true
	cfg.Options.RelaysEnabled = true
	cfg.Options.NATEnabled = true

	// Disable only privacy-invasive and self-management features
	cfg.Options.URAccepted = -1           // no usage reporting
	cfg.Options.CREnabled = false         // no crash reporting
	cfg.Options.CRURL = ""                // blank out crash-report endpoint
	cfg.Options.AutoUpgradeIntervalH = 0  // we manage our own binary
	cfg.Options.StartBrowser = false      // headless

	// Write config.xml with restricted permissions (contains API key)
	fd, err := os.OpenFile(configFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer fd.Close()

	wrapper := config.Wrap(configFile, cfg, deviceID, events.NoopLogger)
	return wrapper.Save()
}

// xmlConfig is a minimal struct for reading API key from config.xml.
type xmlConfig struct {
	XMLName xml.Name `xml:"configuration"`
	GUI     xmlGUI   `xml:"gui"`
}

type xmlGUI struct {
	APIKey string `xml:"apikey"`
}

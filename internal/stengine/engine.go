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
	LocalAnnPort = 11027
)

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

	// Set Syncthing's locations to our isolated paths. SetBaseDir only errors
	// on unknown BaseDirEnum values, which we're passing as library constants,
	// so the returned error is structurally impossible here.
	_ = locations.SetBaseDir(locations.ConfigBaseDir, e.configDir)
	_ = locations.SetBaseDir(locations.DataBaseDir, e.dataDir)

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
	// SetBaseDir only errors on unknown BaseDirEnum values; library constants
	// cannot trigger that branch.
	_ = locations.SetBaseDir(locations.ConfigBaseDir, e.configDir)
	_ = locations.SetBaseDir(locations.DataBaseDir, e.dataDir)

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

	// Informational banner; losing this to a closed stdout isn't worth failing on.
	_, _ = fmt.Fprintln(ourStdout, "[dotkeeper] embedded Syncthing started on", GUIAddress)

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
	// TCP-only. QUIC is deliberately disabled:
	//   1. quic-go v0.52.0 (which Syncthing v1.30.0 pins) panics with
	//      "crypto/tls bug: where's my session ticket?" at startup under
	//      certain peer-state conditions, producing a restart loop.
	//   2. Both outstanding CVEs tracked in SECURITY.md (GO-2025-4017,
	//      GO-2025-4233) are in quic-go. Not listening on QUIC makes
	//      them unreachable code.
	// When Syncthing upstream bumps past quic-go v0.54.1 we can revisit.
	cfg.Options.RawListenAddresses = []string{ListenTCP}

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
	defer func() { _ = fd.Close() }()

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

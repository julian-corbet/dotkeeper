// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"fmt"
	"runtime"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/service"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// STClient is the subset of stclient.Client the doctor checks depend on.
// Using an interface lets tests substitute a deterministic fake and
// keeps the doctor package free of network dependencies during unit
// testing.
type STClient interface {
	Ping() error
	GetStatus() (*stclient.SystemStatus, error)
	GetConfig() (map[string]any, error)
	GetConnections() (*stclient.Connections, error)
	GetFolderStatus(folderID string) (*stclient.FolderStatus, error)
}

// --- 1. version -------------------------------------------------------

// VersionCheck reports the running binary's version and commit. Always
// OK — the point is to make the doctor output self-describing when it
// gets pasted into an issue report.
type VersionCheck struct {
	Version string
	Commit  string
}

func (VersionCheck) Name() string { return "version" }
func (c VersionCheck) Run(_ context.Context) Result {
	return Result{
		Name:    "version",
		Outcome: OK,
		Detail:  fmt.Sprintf("dotkeeper %s (%s) on %s/%s", c.Version, c.Commit, runtime.GOOS, runtime.GOARCH),
	}
}

// --- 2. config --------------------------------------------------------

// ConfigCheck loads machine.toml and config.toml. Fails if either is
// missing or unparseable (dotkeeper can't function without them). Warns
// when machine.toml's name is not registered in the shared config's
// machines table — usually a sign that `dotkeeper pair` hasn't run yet
// or the config sync hasn't caught up.
type ConfigCheck struct {
	// Loader overrides let tests inject fake configs; when nil, the
	// real on-disk loaders are used.
	LoadMachine func() (*config.MachineConfig, error)
	LoadShared  func() (*config.SharedConfig, error)
}

func (ConfigCheck) Name() string { return "config" }
func (c ConfigCheck) Run(_ context.Context) Result {
	loadM := c.LoadMachine
	if loadM == nil {
		loadM = config.LoadMachineConfig
	}
	loadS := c.LoadShared
	if loadS == nil {
		loadS = config.LoadSharedConfig
	}

	m, err := loadM()
	if err != nil {
		return Result{Name: "config", Outcome: Fail, Detail: "machine.toml: " + err.Error(), Hint: "check file contents at " + config.MachineConfigPath()}
	}
	if m == nil {
		return Result{Name: "config", Outcome: Fail, Detail: "machine.toml missing", Hint: "run 'dotkeeper init'"}
	}

	cfg, err := loadS()
	if err != nil {
		return Result{Name: "config", Outcome: Fail, Detail: "config.toml: " + err.Error(), Hint: "check file contents at " + config.SharedConfigPath()}
	}
	if cfg == nil {
		return Result{Name: "config", Outcome: Fail, Detail: "config.toml missing", Hint: "run 'dotkeeper init' or join an existing setup"}
	}

	// Warn-path: machine identity not in shared config yet.
	found := false
	for _, entry := range cfg.Machines {
		if entry.Hostname == m.Name {
			found = true
			break
		}
	}
	if !found {
		return Result{
			Name:    "config",
			Outcome: Warn,
			Detail:  fmt.Sprintf("machine %q not in config registry (%d repos tracked)", m.Name, len(cfg.Repos)),
			Hint:    "run 'dotkeeper pair' — shared config may not be synced yet",
		}
	}
	return Result{
		Name:    "config",
		Outcome: OK,
		Detail:  fmt.Sprintf("machine %q (slot %d), %d managed folders", m.Name, m.Slot, len(cfg.Repos)),
	}
}

// --- 3. service -------------------------------------------------------

// ServiceCheck queries the platform service manager for the Syncthing
// unit state. OK when active/running, Warn when inactive (user hasn't
// started it), Fail when failed/error.
//
// When the platform has no service manager (noop backend) or the unit
// state is simply unavailable, the check reports Warn with a note — it
// can't verify the service, but that's a dotkeeper configuration gap,
// not a Syncthing failure.
type ServiceCheck struct {
	Manager service.Manager
}

func (ServiceCheck) Name() string { return "service" }
func (c ServiceCheck) Run(_ context.Context) Result {
	if c.Manager == nil {
		return Result{Name: "service", Outcome: Warn, Detail: "no service manager available (manual mode)"}
	}

	// Prefer the rich status when the backend exposes it. Only systemd
	// does today; cron/launchd/windows fall back to the boolean API.
	if rich, ok := c.Manager.(interface{ SyncthingStatus() service.SyncthingUnitStatus }); ok {
		st := rich.SyncthingStatus()
		return interpretSyncthingStatus(c.Manager.Name(), st)
	}

	if c.Manager.IsSyncthingRunning() {
		return Result{
			Name:    "service",
			Outcome: OK,
			Detail:  fmt.Sprintf("dotkeeper-syncthing running (%s)", c.Manager.Name()),
		}
	}
	return Result{
		Name:    "service",
		Outcome: Warn,
		Detail:  fmt.Sprintf("dotkeeper-syncthing not running (%s)", c.Manager.Name()),
		Hint:    "start it with 'dotkeeper start' or your platform's service command",
	}
}

func interpretSyncthingStatus(backend string, st service.SyncthingUnitStatus) Result {
	switch st.Active {
	case "active":
		detail := fmt.Sprintf("dotkeeper-syncthing.service active (%s)", st.Sub)
		if !st.Since.IsZero() {
			detail = fmt.Sprintf("dotkeeper-syncthing.service active (%s since %s)",
				st.Sub, st.Since.Format("2006-01-02 15:04:05"))
		}
		return Result{Name: "service", Outcome: OK, Detail: detail}
	case "failed":
		return Result{
			Name:    "service",
			Outcome: Fail,
			Detail:  "dotkeeper-syncthing.service failed",
			Hint:    "check logs: journalctl --user -u dotkeeper-syncthing.service",
		}
	case "inactive", "deactivating":
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  "dotkeeper-syncthing.service inactive",
			Hint:    "start it: systemctl --user start dotkeeper-syncthing.service",
		}
	case "activating":
		return Result{Name: "service", Outcome: Warn, Detail: "dotkeeper-syncthing.service starting…"}
	case "":
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  fmt.Sprintf("dotkeeper-syncthing unit status unknown (%s backend)", backend),
		}
	default:
		return Result{
			Name:    "service",
			Outcome: Warn,
			Detail:  fmt.Sprintf("dotkeeper-syncthing.service %s", st.Active),
		}
	}
}

// --- 4. syncthing API --------------------------------------------------

// SyncthingAPICheck pings the Syncthing REST API. OK when /rest/system/ping
// returns 200; Fail otherwise. This check is a prerequisite for peers
// and folders — when it fails, those later checks will fail too but
// the hint on *this* check is the actionable one.
type SyncthingAPICheck struct {
	Client STClient
}

func (SyncthingAPICheck) Name() string { return "syncthing API" }
func (c SyncthingAPICheck) Run(_ context.Context) Result {
	if c.Client == nil {
		return Result{Name: "syncthing API", Outcome: Fail, Detail: "client not available", Hint: "dotkeeper not initialised — run 'dotkeeper init'"}
	}
	if err := c.Client.Ping(); err != nil {
		return Result{
			Name:    "syncthing API",
			Outcome: Fail,
			Detail:  fmt.Sprintf("%s unreachable (%v)", stclient.APIAddress, err),
			Hint:    "is dotkeeper-syncthing.service running?",
		}
	}
	return Result{
		Name:    "syncthing API",
		Outcome: OK,
		Detail:  "reachable at " + stclient.APIAddress,
	}
}

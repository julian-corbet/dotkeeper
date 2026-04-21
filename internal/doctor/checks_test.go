// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/service"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
)

// --- Fakes ------------------------------------------------------------

// fakeST implements STClient. Each call returns the configured value.
type fakeST struct {
	pingErr error

	status    *stclient.SystemStatus
	statusErr error

	config    map[string]any
	configErr error

	conns    *stclient.Connections
	connsErr error

	folderStatus    map[string]*stclient.FolderStatus
	folderStatusErr error
}

func (f *fakeST) Ping() error { return f.pingErr }
func (f *fakeST) GetStatus() (*stclient.SystemStatus, error) {
	return f.status, f.statusErr
}
func (f *fakeST) GetConfig() (map[string]any, error) {
	return f.config, f.configErr
}
func (f *fakeST) GetConnections() (*stclient.Connections, error) {
	return f.conns, f.connsErr
}
func (f *fakeST) GetFolderStatus(id string) (*stclient.FolderStatus, error) {
	if f.folderStatusErr != nil {
		return nil, f.folderStatusErr
	}
	if s, ok := f.folderStatus[id]; ok {
		return s, nil
	}
	return &stclient.FolderStatus{State: "idle"}, nil
}

// fakeManager implements service.Manager with programmable state.
type fakeManager struct {
	name        string
	running     bool
	timerActive bool
}

func (f *fakeManager) Name() string                              { return f.name }
func (f *fakeManager) InstallSyncthing(string) error             { return nil }
func (f *fakeManager) InstallTimer(string, string, string) error { return nil }
func (f *fakeManager) StartSyncthing() error                     { return nil }
func (f *fakeManager) StopSyncthing() error                      { return nil }
func (f *fakeManager) IsSyncthingRunning() bool                  { return f.running }
func (f *fakeManager) IsTimerActive() bool                       { return f.timerActive }
func (f *fakeManager) DaemonReload() error                       { return nil }

// richManager layers the optional SyncthingStatus/TimerNext interfaces
// onto a fakeManager — exercising the code paths the doctor uses when
// the backend provides richer information (systemd, today).
type richManager struct {
	*fakeManager
	st    service.SyncthingUnitStatus
	timer service.TimerNextRun
}

func (r *richManager) SyncthingStatus() service.SyncthingUnitStatus { return r.st }
func (r *richManager) TimerNext() service.TimerNextRun              { return r.timer }

// --- Version ----------------------------------------------------------

func TestVersionCheck(t *testing.T) {
	r := VersionCheck{Version: "1.2.3", Commit: "abc1234"}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK", r.Outcome)
	}
	if !strings.Contains(r.Detail, "1.2.3") || !strings.Contains(r.Detail, "abc1234") {
		t.Errorf("Detail missing version/commit: %q", r.Detail)
	}
}

// --- Config -----------------------------------------------------------

func TestConfigCheckOK(t *testing.T) {
	c := ConfigCheck{
		LoadMachine: func() (*config.MachineConfig, error) {
			return &config.MachineConfig{Name: "host-a", Slot: 0}, nil
		},
		LoadShared: func() (*config.SharedConfig, error) {
			return &config.SharedConfig{
				Machines: map[string]config.MachineEntry{"host_a": {Hostname: "host-a", Slot: 0, SyncthingID: "X"}},
				Repos:    []config.RepoEntry{{Name: "r1", Path: "/tmp/r1"}},
			}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, Detail=%q, want OK", r.Outcome, r.Detail)
	}
}

func TestConfigCheckMissingMachine(t *testing.T) {
	c := ConfigCheck{
		LoadMachine: func() (*config.MachineConfig, error) { return nil, nil },
		LoadShared:  func() (*config.SharedConfig, error) { return &config.SharedConfig{}, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

func TestConfigCheckMissingShared(t *testing.T) {
	c := ConfigCheck{
		LoadMachine: func() (*config.MachineConfig, error) {
			return &config.MachineConfig{Name: "h"}, nil
		},
		LoadShared: func() (*config.SharedConfig, error) { return nil, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

func TestConfigCheckWarnOnUnregisteredMachine(t *testing.T) {
	c := ConfigCheck{
		LoadMachine: func() (*config.MachineConfig, error) {
			return &config.MachineConfig{Name: "stranger", Slot: 3}, nil
		},
		LoadShared: func() (*config.SharedConfig, error) {
			return &config.SharedConfig{
				Machines: map[string]config.MachineEntry{"other": {Hostname: "other", SyncthingID: "Z"}},
			}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

func TestConfigCheckFailOnMachineLoadError(t *testing.T) {
	c := ConfigCheck{
		LoadMachine: func() (*config.MachineConfig, error) { return nil, errors.New("parse error") },
		LoadShared:  func() (*config.SharedConfig, error) { return &config.SharedConfig{}, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

// --- Service ----------------------------------------------------------

func TestServiceCheckNilManager(t *testing.T) {
	r := ServiceCheck{}.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

func TestServiceCheckBooleanBackend(t *testing.T) {
	r := ServiceCheck{Manager: &fakeManager{name: "cron", running: true}}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK", r.Outcome)
	}
	r2 := ServiceCheck{Manager: &fakeManager{name: "cron", running: false}}.Run(context.Background())
	if r2.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r2.Outcome)
	}
}

func TestServiceCheckRichActive(t *testing.T) {
	m := &richManager{
		fakeManager: &fakeManager{name: "systemd", running: true},
		st: service.SyncthingUnitStatus{
			Active: "active", Sub: "running",
			Since: time.Date(2026, 4, 19, 15, 21, 13, 0, time.Local),
		},
	}
	r := ServiceCheck{Manager: m}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK", r.Outcome)
	}
	if !strings.Contains(r.Detail, "running") {
		t.Errorf("detail missing sub-state: %q", r.Detail)
	}
}

func TestServiceCheckRichFailed(t *testing.T) {
	m := &richManager{
		fakeManager: &fakeManager{name: "systemd"},
		st:          service.SyncthingUnitStatus{Active: "failed", Sub: "failed"},
	}
	r := ServiceCheck{Manager: m}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

func TestServiceCheckRichInactive(t *testing.T) {
	m := &richManager{
		fakeManager: &fakeManager{name: "systemd"},
		st:          service.SyncthingUnitStatus{Active: "inactive", Sub: "dead"},
	}
	r := ServiceCheck{Manager: m}.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

// --- Syncthing API ----------------------------------------------------

func TestSyncthingAPICheckOK(t *testing.T) {
	r := SyncthingAPICheck{Client: &fakeST{}}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK", r.Outcome)
	}
}

func TestSyncthingAPICheckFail(t *testing.T) {
	r := SyncthingAPICheck{Client: &fakeST{pingErr: errors.New("connection refused")}}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
	if r.Hint == "" {
		t.Errorf("expected a hint on failure")
	}
}

func TestSyncthingAPICheckNilClient(t *testing.T) {
	r := SyncthingAPICheck{Client: nil}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

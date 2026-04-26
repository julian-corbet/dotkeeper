// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/conflict"
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

// fakeGit implements GitRunner with programmable per-dir responses.
type fakeGit struct {
	byDir map[string]struct {
		out     string
		err     error
		timeout bool
	}
}

func (f *fakeGit) LsRemote(ctx context.Context, dir string) (string, error) {
	r, ok := f.byDir[dir]
	if !ok {
		return "", errors.New("no fake for " + dir)
	}
	if r.timeout {
		// Simulate timeout by blocking until ctx deadline exceeds.
		<-ctx.Done()
		return "", ctx.Err()
	}
	return r.out, r.err
}

// fakeManager implements the boolean service.Manager surface.
// For the optional SyncthingStatus interface, see richManager.
type fakeManager struct {
	name    string
	running bool
}

func (f *fakeManager) Name() string                  { return f.name }
func (f *fakeManager) InstallSyncthing(string) error { return nil }
func (f *fakeManager) StartSyncthing() error         { return nil }
func (f *fakeManager) StopSyncthing() error          { return nil }
func (f *fakeManager) IsSyncthingRunning() bool      { return f.running }
func (f *fakeManager) DaemonReload() error           { return nil }

type richManager struct {
	*fakeManager
	st service.SyncthingUnitStatus
}

func (r *richManager) SyncthingStatus() service.SyncthingUnitStatus { return r.st }

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

// TestConfigCheckPassesWithValidV5Files verifies that ConfigCheck reports OK
// when machine.toml v2 and state.toml are both present and valid with a
// device ID and at least one scan root.
func TestConfigCheckPassesWithValidV5Files(t *testing.T) {
	c := ConfigCheck{
		LoadMachineV2: func() (*config.MachineConfigV2, error) {
			return &config.MachineConfigV2{
				SchemaVersion: 2,
				Name:          "test-machine",
				Slot:          0,
				Discovery: config.DiscoveryConfig{
					ScanRoots: []string{"~/Documents"},
				},
			}, nil
		},
		LoadStateV2: func() (*config.StateV2, error) {
			return &config.StateV2{
				SchemaVersion:     2,
				SyncthingDeviceID: "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH",
			}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, Detail=%q, want OK", r.Outcome, r.Detail)
	}
}

// TestConfigCheckOK is an alias for TestConfigCheckPassesWithValidV5Files
// for backward compatibility in test naming.
func TestConfigCheckOK(t *testing.T) {
	TestConfigCheckPassesWithValidV5Files(t)
}

// TestConfigCheckMissingMachine verifies that a missing machine.toml yields Fail.
func TestConfigCheckMissingMachine(t *testing.T) {
	c := ConfigCheck{
		LoadMachineV2: func() (*config.MachineConfigV2, error) { return nil, nil },
		LoadStateV2: func() (*config.StateV2, error) {
			return &config.StateV2{SyncthingDeviceID: "AAAAAAA-X"}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

// TestConfigCheckFailsOnMissingState verifies that a missing state.toml yields Fail.
func TestConfigCheckFailsOnMissingState(t *testing.T) {
	c := ConfigCheck{
		LoadMachineV2: func() (*config.MachineConfigV2, error) {
			return &config.MachineConfigV2{
				SchemaVersion: 2,
				Name:          "h",
				Discovery:     config.DiscoveryConfig{ScanRoots: []string{"~/Documents"}},
			}, nil
		},
		LoadStateV2: func() (*config.StateV2, error) { return nil, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

// TestConfigCheckMissingShared is an alias for TestConfigCheckFailsOnMissingState.
func TestConfigCheckMissingShared(t *testing.T) {
	TestConfigCheckFailsOnMissingState(t)
}

// TestConfigCheckWarnOnUnregisteredMachine verifies that a machine with no
// scan roots configured yields Warn (advisory, not Fail).
func TestConfigCheckWarnOnUnregisteredMachine(t *testing.T) {
	c := ConfigCheck{
		LoadMachineV2: func() (*config.MachineConfigV2, error) {
			return &config.MachineConfigV2{
				SchemaVersion: 2,
				Name:          "stranger",
				Slot:          3,
				Discovery:     config.DiscoveryConfig{}, // no scan roots
			}, nil
		},
		LoadStateV2: func() (*config.StateV2, error) {
			return &config.StateV2{SyncthingDeviceID: "AAAAAAA-X"}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

// TestConfigCheckFailOnMachineLoadError verifies that a machine.toml parse
// error yields Fail.
func TestConfigCheckFailOnMachineLoadError(t *testing.T) {
	c := ConfigCheck{
		LoadMachineV2: func() (*config.MachineConfigV2, error) {
			return nil, errors.New("parse error")
		},
		LoadStateV2: func() (*config.StateV2, error) {
			return &config.StateV2{SyncthingDeviceID: "AAAAAAA-X"}, nil
		},
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

// --- Peers ------------------------------------------------------------

func TestPeersCheckAllConnected(t *testing.T) {
	state := &config.StateV2{
		SchemaVersion:     2,
		SyncthingDeviceID: "ME-ID",
		Peers: []config.PeerEntry{
			{Name: "me", DeviceID: "ME-ID"},
			{Name: "other", DeviceID: "OTHER-ID"},
		},
	}
	st := &fakeST{
		status: &stclient.SystemStatus{MyID: "ME-ID"},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{
			"OTHER-ID": {Connected: true},
		}},
	}
	r := PeersCheck{Client: st, LoadState: func() (*config.StateV2, error) { return state, nil }}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK; detail=%q", r.Outcome, r.Detail)
	}
}

func TestPeersCheckSomeOffline(t *testing.T) {
	state := &config.StateV2{
		SchemaVersion:     2,
		SyncthingDeviceID: "ME-ID",
		Peers: []config.PeerEntry{
			{Name: "me", DeviceID: "ME-ID"},
			{Name: "other", DeviceID: "OTHER-ID"},
			{Name: "third", DeviceID: "THIRD-ID"},
		},
	}
	st := &fakeST{
		status: &stclient.SystemStatus{MyID: "ME-ID"},
		conns: &stclient.Connections{Connections: map[string]stclient.Connection{
			"OTHER-ID": {Connected: true},
			"THIRD-ID": {Connected: false},
		}},
	}
	r := PeersCheck{Client: st, LoadState: func() (*config.StateV2, error) { return state, nil }}.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn; detail=%q", r.Outcome, r.Detail)
	}
	if !strings.Contains(r.Detail, "third") {
		t.Errorf("expected 'third' in offline list, got: %q", r.Detail)
	}
}

func TestPeersCheckSoloMachine(t *testing.T) {
	state := &config.StateV2{
		SchemaVersion:     2,
		SyncthingDeviceID: "ME-ID",
		Peers: []config.PeerEntry{
			{Name: "me", DeviceID: "ME-ID"},
		},
	}
	st := &fakeST{
		status: &stclient.SystemStatus{MyID: "ME-ID"},
		conns:  &stclient.Connections{Connections: map[string]stclient.Connection{}},
	}
	r := PeersCheck{Client: st, LoadState: func() (*config.StateV2, error) { return state, nil }}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK (single-machine)", r.Outcome)
	}
}

func TestPeersCheckAPIFailure(t *testing.T) {
	state := &config.StateV2{SchemaVersion: 2, Peers: []config.PeerEntry{}}
	st := &fakeST{statusErr: errors.New("boom")}
	r := PeersCheck{Client: st, LoadState: func() (*config.StateV2, error) { return state, nil }}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

// --- Folders ----------------------------------------------------------

func TestFoldersCheckAllIdle(t *testing.T) {
	st := &fakeST{
		config: map[string]any{
			"folders": []any{
				map[string]any{"id": "f1"},
				map[string]any{"id": "f2"},
			},
		},
		folderStatus: map[string]*stclient.FolderStatus{
			"f1": {State: "idle"},
			"f2": {State: "idle"},
		},
	}
	r := FoldersCheck{Client: st}.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK; detail=%q", r.Outcome, r.Detail)
	}
}

func TestFoldersCheckSyncingWarns(t *testing.T) {
	st := &fakeST{
		config: map[string]any{
			"folders": []any{map[string]any{"id": "f1"}, map[string]any{"id": "f2"}},
		},
		folderStatus: map[string]*stclient.FolderStatus{
			"f1": {State: "idle"},
			"f2": {State: "syncing"},
		},
	}
	r := FoldersCheck{Client: st}.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

func TestFoldersCheckErrorFails(t *testing.T) {
	st := &fakeST{
		config: map[string]any{
			"folders": []any{map[string]any{"id": "f1"}, map[string]any{"id": "f2"}},
		},
		folderStatus: map[string]*stclient.FolderStatus{
			"f1": {State: "idle"},
			"f2": {State: "error"},
		},
	}
	r := FoldersCheck{Client: st}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
	if !strings.Contains(r.Detail, "f2") {
		t.Errorf("detail should include failing folder id: %q", r.Detail)
	}
}

func TestFoldersCheckStoppedFails(t *testing.T) {
	st := &fakeST{
		config: map[string]any{"folders": []any{map[string]any{"id": "f1"}}},
		folderStatus: map[string]*stclient.FolderStatus{
			"f1": {State: "stopped"},
		},
	}
	r := FoldersCheck{Client: st}.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
}

func TestFoldersCheckNoFoldersWarns(t *testing.T) {
	st := &fakeST{config: map[string]any{"folders": []any{}}}
	r := FoldersCheck{Client: st}.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
}

// --- Git remotes ------------------------------------------------------

func TestGitRemotesCheckOK(t *testing.T) {
	dir := t.TempDir()
	state := &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			dir: {},
		},
	}
	runner := &fakeGit{byDir: map[string]struct {
		out     string
		err     error
		timeout bool
	}{dir: {out: "ok\n"}}}
	c := GitRemotesCheck{
		Runner:    runner,
		LoadState: func() (*config.StateV2, error) { return state, nil },
		Timeout:   time.Second,
	}
	r := c.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK; detail=%q", r.Outcome, r.Detail)
	}
}

func TestGitRemotesCheckAuthFails(t *testing.T) {
	dir := t.TempDir()
	state := &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			dir: {},
		},
	}
	runner := &fakeGit{byDir: map[string]struct {
		out     string
		err     error
		timeout bool
	}{dir: {out: "Permission denied (publickey)", err: errors.New("exit 128")}}}
	c := GitRemotesCheck{
		Runner:    runner,
		LoadState: func() (*config.StateV2, error) { return state, nil },
		Timeout:   time.Second,
	}
	r := c.Run(context.Background())
	if r.Outcome != Fail {
		t.Errorf("Outcome = %v, want Fail", r.Outcome)
	}
	if !strings.Contains(r.Detail, "auth") {
		t.Errorf("expected auth classification in detail: %q", r.Detail)
	}
}

func TestGitRemotesCheckTimeoutWarns(t *testing.T) {
	dir := t.TempDir()
	state := &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{
			dir: {},
		},
	}
	runner := &fakeGit{byDir: map[string]struct {
		out     string
		err     error
		timeout bool
	}{dir: {timeout: true}}}
	c := GitRemotesCheck{
		Runner:    runner,
		LoadState: func() (*config.StateV2, error) { return state, nil },
		Timeout:   50 * time.Millisecond,
	}
	r := c.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn; detail=%q", r.Outcome, r.Detail)
	}
	if !strings.Contains(r.Detail, "timeout") {
		t.Errorf("expected 'timeout' in detail: %q", r.Detail)
	}
}

func TestGitRemotesCheckNoGitRepos(t *testing.T) {
	state := &config.StateV2{
		SchemaVersion: 2,
		ObservedRepos: map[string]config.ObservedRepo{},
	}
	c := GitRemotesCheck{
		Runner:    &fakeGit{},
		LoadState: func() (*config.StateV2, error) { return state, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK", r.Outcome)
	}
	if !strings.Contains(r.Detail, "no observed") {
		t.Errorf("expected 'no observed' in detail: %q", r.Detail)
	}
}

// --- Conflicts --------------------------------------------------------

func TestConflictsCheckNoneFound(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(tmp, 0o755)
	c := ConflictsCheck{
		FolderProvider: func() []string { return []string{tmp} },
		Scanner:        func(root string) ([]conflict.Conflict, error) { return nil, nil },
	}
	r := c.Run(context.Background())
	if r.Outcome != OK {
		t.Errorf("Outcome = %v, want OK; detail=%q", r.Outcome, r.Detail)
	}
}

func TestConflictsCheckWarnsWhenFound(t *testing.T) {
	tmp := t.TempDir()
	c := ConflictsCheck{
		FolderProvider: func() []string { return []string{tmp} },
		Scanner: func(root string) ([]conflict.Conflict, error) {
			return []conflict.Conflict{{Path: filepath.Join(root, "file.sync-conflict-x")}}, nil
		},
	}
	r := c.Run(context.Background())
	if r.Outcome != Warn {
		t.Errorf("Outcome = %v, want Warn", r.Outcome)
	}
	if r.Hint == "" {
		t.Errorf("expected remediation hint")
	}
}

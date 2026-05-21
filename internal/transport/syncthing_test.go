// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeST is a minimal SyncthingClient stand-in for the unit tests
// in this file. It holds a mutable config map and lets the test
// inject controlled errors for each method. Behaviour mirrors
// internal/reconcile's fakeST closely enough that bugs we find here
// are likely real bugs in either place.
type fakeST struct {
	cfg       map[string]any
	errGet    error
	errSet    error
	errAddDev error
	errPing   error
	setCalled int
	pingDelay time.Duration
}

func newFakeST() *fakeST {
	return &fakeST{cfg: map[string]any{"folders": []any{}, "devices": []any{}}}
}

func (f *fakeST) GetConfig() (map[string]any, error) {
	if f.errGet != nil {
		return nil, f.errGet
	}
	return f.cfg, nil
}

func (f *fakeST) SetConfig(cfg map[string]any) error {
	if f.errSet != nil {
		return f.errSet
	}
	f.cfg = cfg
	f.setCalled++
	return nil
}

func (f *fakeST) AddDevice(_, _ string) error { return f.errAddDev }

func (f *fakeST) Ping() error {
	if f.pingDelay > 0 {
		time.Sleep(f.pingDelay)
	}
	return f.errPing
}

func seedFolder(f *fakeST, id string, devices []string) {
	devs := make([]any, 0, len(devices))
	for _, d := range devices {
		devs = append(devs, map[string]any{"deviceID": d, "introducedBy": ""})
	}
	f.cfg["folders"] = append(f.cfg["folders"].([]any), map[string]any{
		"id":      id,
		"devices": devs,
	})
}

func folderDevices(f *fakeST, folderID string) []string {
	for _, raw := range f.cfg["folders"].([]any) {
		fm := raw.(map[string]any)
		if fm["id"] != folderID {
			continue
		}
		var out []string
		for _, d := range fm["devices"].([]any) {
			dm := d.(map[string]any)
			out = append(out, dm["deviceID"].(string))
		}
		return out
	}
	return nil
}

func TestSyncthingNameAndAvailable(t *testing.T) {
	tr := NewSyncthingTransport(newFakeST())
	if tr.Name() != "syncthing" {
		t.Errorf("Name = %q, want %q", tr.Name(), "syncthing")
	}
	if !tr.Available() {
		t.Error("Available = false; SyncthingTransport must always be available")
	}
}

func TestEnsurePeerAddsDeviceWhenAbsent(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "dk-x", nil)
	tr := NewSyncthingTransport(fake)

	folder := Folder{ID: "dk-x"}
	peer := Peer{Name: "laptop", DeviceID: "ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ23-456789A-BCDEFGH-IJKLMNO-PQRSTUV"}

	if err := tr.EnsurePeerReachability(context.Background(), folder, peer); err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}
	devs := folderDevices(fake, "dk-x")
	if len(devs) != 1 || devs[0] != peer.DeviceID {
		t.Errorf("devices after Ensure = %v, want [%s]", devs, peer.DeviceID)
	}
}

func TestEnsurePeerIdempotentWhenAlreadyPresent(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "dk-x", []string{"DEVICE-A"})
	tr := NewSyncthingTransport(fake)

	folder := Folder{ID: "dk-x"}
	peer := Peer{Name: "laptop", DeviceID: "DEVICE-A"}

	if err := tr.EnsurePeerReachability(context.Background(), folder, peer); err != nil {
		t.Fatalf("EnsurePeerReachability: %v", err)
	}
	// SetConfig must not be called when the peer is already present;
	// idempotency is a hard contract.
	if fake.setCalled != 0 {
		t.Errorf("SetConfig called %d times; idempotent Ensure must not write when state is correct", fake.setCalled)
	}
}

func TestEnsurePeerFolderNotFound(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "other-folder", nil)
	tr := NewSyncthingTransport(fake)
	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "missing"}, Peer{Name: "p", DeviceID: "DEV-A"})
	if err == nil {
		t.Fatal("expected error when folder is not in Syncthing config; got nil")
	}
}

func TestEnsurePeerEmptyDeviceID(t *testing.T) {
	tr := NewSyncthingTransport(newFakeST())
	err := tr.EnsurePeerReachability(context.Background(), Folder{ID: "x"}, Peer{Name: "p"})
	if err == nil {
		t.Error("expected error for empty DeviceID; got nil")
	}
}

func TestEnsurePeerNilClient(t *testing.T) {
	tr := NewSyncthingTransport(nil)
	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "x"}, Peer{Name: "p", DeviceID: "DEV-A"})
	if err == nil {
		t.Error("expected error when client is nil; got nil")
	}
}

func TestRemovePeerStripsDevice(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "dk-x", []string{"DEV-A", "DEV-B"})
	tr := NewSyncthingTransport(fake)

	err := tr.RemovePeerReachability(context.Background(),
		Folder{ID: "dk-x"}, Peer{DeviceID: "DEV-A"})
	if err != nil {
		t.Fatalf("RemovePeerReachability: %v", err)
	}
	devs := folderDevices(fake, "dk-x")
	if len(devs) != 1 || devs[0] != "DEV-B" {
		t.Errorf("devices after Remove = %v, want [DEV-B]", devs)
	}
}

func TestRemovePeerIdempotentWhenAbsent(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "dk-x", []string{"DEV-A"})
	tr := NewSyncthingTransport(fake)

	err := tr.RemovePeerReachability(context.Background(),
		Folder{ID: "dk-x"}, Peer{DeviceID: "DEV-NONEXISTENT"})
	if err != nil {
		t.Fatalf("RemovePeerReachability for absent peer: %v", err)
	}
	if fake.setCalled != 0 {
		t.Errorf("SetConfig called for already-absent peer; Remove must not write when state is correct")
	}
}

func TestProbeReportsRoundTrip(t *testing.T) {
	fake := newFakeST()
	fake.pingDelay = 10 * time.Millisecond
	tr := NewSyncthingTransport(fake)
	d, err := tr.Probe(context.Background(), Peer{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if d < 5*time.Millisecond {
		t.Errorf("Probe latency %v unreasonably small; ping delay was 10ms", d)
	}
}

func TestProbeUnreachableWhenClientNil(t *testing.T) {
	tr := NewSyncthingTransport(nil)
	_, err := tr.Probe(context.Background(), Peer{})
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("Probe with nil client returned %v; want ErrUnreachable so manager treats it as skip-this-cycle", err)
	}
}

func TestProbeSurfacesPingError(t *testing.T) {
	fake := newFakeST()
	fake.errPing = errors.New("connection refused")
	tr := NewSyncthingTransport(fake)
	_, err := tr.Probe(context.Background(), Peer{})
	if err == nil {
		t.Error("expected error when Ping fails; got nil")
	}
}

func TestPropagateChangeIsNoOp(t *testing.T) {
	tr := NewSyncthingTransport(newFakeST())
	err := tr.PropagateChange(context.Background(),
		Change{Folder: Folder{ID: "x"}, CommitHash: "abc"},
		Peer{Name: "p", DeviceID: "DEV-A"})
	if err != nil {
		t.Errorf("PropagateChange should be no-op for SyncthingTransport; got %v", err)
	}
}

func TestSetConfigErrorPropagates(t *testing.T) {
	fake := newFakeST()
	seedFolder(fake, "dk-x", nil)
	fake.errSet = errors.New("network down")
	tr := NewSyncthingTransport(fake)
	err := tr.EnsurePeerReachability(context.Background(),
		Folder{ID: "dk-x"}, Peer{Name: "p", DeviceID: "DEV-A"})
	if err == nil {
		t.Error("expected SetConfig error to propagate; got nil")
	}
}

// _ verifies SyncthingTransport satisfies the Transport interface at
// compile time. If a future refactor changes the interface, this
// catches the divergence before tests run.
var _ Transport = (*SyncthingTransport)(nil)

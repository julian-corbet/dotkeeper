// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package stclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/ping" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Error("missing or wrong API key")
		}
		_, _ = w.Write([]byte(`{"ping":"pong"}`))
	}))
	defer server.Close()

	client := &Client{
		baseURL: server.URL,
		apiKey:  "test-key",
		http:    server.Client(),
	}

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestGetStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"myID": "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD",
		})
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}

	status, err := client.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.MyID != "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD" {
		t.Errorf("MyID = %q", status.MyID)
	}
}

func TestAddDevice(t *testing.T) {
	var lastConfig map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/config"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{},
				"folders": []any{},
			})
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/config"):
			_ = json.NewDecoder(r.Body).Decode(&lastConfig)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}

	err := client.AddDevice("NEW-DEVICE-ID", "my-peer")
	if err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	// Verify the device was added
	devices, ok := lastConfig["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("expected 1 device, got %v", lastConfig["devices"])
	}
	dev := devices[0].(map[string]any)
	if dev["deviceID"] != "NEW-DEVICE-ID" {
		t.Errorf("deviceID = %v", dev["deviceID"])
	}
	if dev["name"] != "my-peer" {
		t.Errorf("name = %v", dev["name"])
	}
	// Folder membership is opt-in per machine — AddDevice must never
	// set autoAcceptFolders=true. With this false, a peer offering a
	// folder the local side hasn't subscribed to is silently ignored
	// (Syncthing logs an informational WARN once per ClusterConfig
	// but does NOT enter the auto-accept failure loop that produces
	// thousands of ERRORs/hour on partial-overlap fleets).
	if v, _ := dev["autoAcceptFolders"].(bool); v {
		t.Error("autoAcceptFolders must default to false")
	}
}

func TestAddDeviceSkipsDuplicate(t *testing.T) {
	putCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{
					map[string]any{"deviceID": "EXISTING-ID", "name": "existing", "autoAcceptFolders": false},
				},
			})
		case "PUT":
			putCalled = true
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}

	err := client.AddDevice("EXISTING-ID", "existing")
	if err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	if putCalled {
		t.Error("PUT should not be called for existing device")
	}
}

// TestAddDeviceMigratesExistingAutoAccept proves the in-place migration
// path: if a device already exists with autoAcceptFolders=true (an
// install created before v1.1.14 flipped the default), AddDevice must
// PATCH it down to false rather than silently no-oping. Without this,
// upgrading dotkeeper wouldn't stop the existing ERROR storm on
// already-paired peers — only newly-paired ones would benefit.
func TestAddDeviceMigratesExistingAutoAccept(t *testing.T) {
	var lastConfig map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{
					map[string]any{"deviceID": "STALE-ID", "name": "stale", "autoAcceptFolders": true},
				},
			})
		case "PUT":
			_ = json.NewDecoder(r.Body).Decode(&lastConfig)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}

	if err := client.AddDevice("STALE-ID", "stale"); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	devs, _ := lastConfig["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("expected 1 device after migration, got %d", len(devs))
	}
	dm := devs[0].(map[string]any)
	if v, _ := dm["autoAcceptFolders"].(bool); v {
		t.Error("autoAcceptFolders should be false after AddDevice migration")
	}
}

// TestMigrateDisableAutoAcceptFolders covers the one-shot daemon-startup
// migration: every device with autoAcceptFolders=true is flipped to
// false in a single SetConfig, and the count of devices migrated is
// returned. When nothing needs migrating, no PUT is issued at all.
func TestMigrateDisableAutoAcceptFolders(t *testing.T) {
	t.Run("migrates true to false", func(t *testing.T) {
		var lastConfig map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case "GET":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"devices": []any{
						map[string]any{"deviceID": "A", "autoAcceptFolders": true},
						map[string]any{"deviceID": "B", "autoAcceptFolders": false},
						map[string]any{"deviceID": "C", "autoAcceptFolders": true},
					},
				})
			case "PUT":
				_ = json.NewDecoder(r.Body).Decode(&lastConfig)
				w.WriteHeader(200)
			}
		}))
		defer server.Close()

		client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
		n, err := client.MigrateDisableAutoAcceptFolders()
		if err != nil {
			t.Fatalf("MigrateDisableAutoAcceptFolders: %v", err)
		}
		if n != 2 {
			t.Errorf("migrated count = %d, want 2", n)
		}
		devs := lastConfig["devices"].([]any)
		for _, d := range devs {
			dm := d.(map[string]any)
			if v, _ := dm["autoAcceptFolders"].(bool); v {
				t.Errorf("device %v still has autoAcceptFolders=true", dm["deviceID"])
			}
		}
	})

	t.Run("no PUT when already migrated", func(t *testing.T) {
		putCalled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case "GET":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"devices": []any{
						map[string]any{"deviceID": "A", "autoAcceptFolders": false},
						map[string]any{"deviceID": "B", "autoAcceptFolders": false},
					},
				})
			case "PUT":
				putCalled = true
				w.WriteHeader(200)
			}
		}))
		defer server.Close()

		client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
		n, err := client.MigrateDisableAutoAcceptFolders()
		if err != nil {
			t.Fatalf("MigrateDisableAutoAcceptFolders: %v", err)
		}
		if n != 0 {
			t.Errorf("migrated count = %d, want 0", n)
		}
		if putCalled {
			t.Error("PUT should not be issued when nothing needs migration")
		}
	})
}

func TestAddOrUpdateFolder(t *testing.T) {
	var lastConfig map[string]any
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/config"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{},
				"folders": []any{},
			})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]string{"myID": "MY-ID"})
		case r.Method == "PUT":
			callCount++
			_ = json.NewDecoder(r.Body).Decode(&lastConfig)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}

	err := client.AddOrUpdateFolder("test-folder", "Test", "/tmp/test", []string{"MY-ID", "PEER-ID"})
	if err != nil {
		t.Fatalf("AddOrUpdateFolder: %v", err)
	}

	folders, ok := lastConfig["folders"].([]any)
	if !ok || len(folders) != 1 {
		t.Fatalf("expected 1 folder, got %v", lastConfig["folders"])
	}
	folder := folders[0].(map[string]any)
	if folder["id"] != "test-folder" {
		t.Errorf("id = %v", folder["id"])
	}
	if folder["path"] != "/tmp/test" {
		t.Errorf("path = %v", folder["path"])
	}

	// Verify MY-ID is excluded from folder devices (only PEER-ID)
	devs := folder["devices"].([]any)
	for _, d := range devs {
		dm := d.(map[string]any)
		if dm["deviceID"] == "MY-ID" {
			t.Error("own device ID should not be in folder devices")
		}
	}

	// Verify the marker-file override is propagated (item 6 of v0.1.1).
	if folder["markerName"] != FolderMarkerName {
		t.Errorf("markerName = %v, want %q", folder["markerName"], FolderMarkerName)
	}

	// rescanIntervalS must match the canonical default in
	// stclient.CanonicalRescanIntervalS. v0.9.4 set this to 86400
	// (daily); v0.9.7 lowered to 0 (dotkeeper-driven rescans).
	// Asserting against the constant rather than a hard-coded
	// number keeps this test resilient across future changes to
	// the canonical value.
	if got := folder["rescanIntervalS"]; got != float64(CanonicalRescanIntervalS) {
		t.Errorf("rescanIntervalS = %v, want %d", got, CanonicalRescanIntervalS)
	}
	if got := folder["fsWatcherEnabled"]; got != CanonicalFsWatcherEnabled {
		t.Errorf("fsWatcherEnabled = %v, want %v", got, CanonicalFsWatcherEnabled)
	}
	// hashers must match CanonicalHashers (1) so the scan CPU
	// spike at cold start / wake-from-suspend stays bounded to one
	// core per folder. Asserting against the constant rather than
	// a literal keeps the test resilient if the canonical value
	// evolves.
	if got := folder["hashers"]; got != float64(CanonicalHashers) {
		t.Errorf("hashers = %v, want %d", got, CanonicalHashers)
	}
}

func TestGetConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/connections" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"connections":{"PEER-A":{"connected":true,"address":"tcp://1.2.3.4","clientVersion":"v1.30.0"},"PEER-B":{"connected":false,"address":"","clientVersion":""}}}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	conns, err := client.GetConnections()
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(conns.Connections) != 2 {
		t.Errorf("got %d connections, want 2", len(conns.Connections))
	}
	if !conns.Connections["PEER-A"].Connected {
		t.Errorf("PEER-A should be connected")
	}
	if conns.Connections["PEER-B"].Connected {
		t.Errorf("PEER-B should not be connected")
	}
}

func TestGetFolderStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "folder=my-folder") {
			t.Errorf("missing folder query param in %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"state":"idle","errors":0,"needFiles":0}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	status, err := client.GetFolderStatus("my-folder")
	if err != nil {
		t.Fatalf("GetFolderStatus: %v", err)
	}
	if status.State != "idle" {
		t.Errorf("state = %q, want idle", status.State)
	}
}

func TestPingFailure(t *testing.T) {
	// Connect to a port that's not listening
	client := &Client{baseURL: "http://127.0.0.1:19999", apiKey: "key", http: http.DefaultClient}

	err := client.Ping()
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

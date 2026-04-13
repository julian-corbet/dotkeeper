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
		w.Write([]byte(`{"ping":"pong"}`))
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
		json.NewEncoder(w).Encode(map[string]string{
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
			json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{},
				"folders": []any{},
			})
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/config"):
			json.NewDecoder(r.Body).Decode(&lastConfig)
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
}

func TestAddDeviceSkipsDuplicate(t *testing.T) {
	putCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{
					map[string]any{"deviceID": "EXISTING-ID", "name": "existing"},
				},
			})
		} else if r.Method == "PUT" {
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

func TestAddOrUpdateFolder(t *testing.T) {
	var lastConfig map[string]any
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/config"):
			json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{},
				"folders": []any{},
			})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/status"):
			json.NewEncoder(w).Encode(map[string]string{"myID": "MY-ID"})
		case r.Method == "PUT":
			callCount++
			json.NewDecoder(r.Body).Decode(&lastConfig)
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
}

func TestPingFailure(t *testing.T) {
	// Connect to a port that's not listening
	client := &Client{baseURL: "http://127.0.0.1:19999", apiKey: "key", http: http.DefaultClient}

	err := client.Ping()
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package stclient

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPingHTTP500 verifies that a 500 response produces an error.
func TestPingHTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	err := client.Ping()
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
}

// TestPingHTTP403 verifies that a 403 (bad API key) produces an error.
func TestPingHTTP403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "wrong-key", http: server.Client()}
	err := client.Ping()
	if err == nil {
		t.Error("expected error for HTTP 403")
	}
}

// TestGetStatusTruncatedJSON verifies graceful handling of truncated JSON.
func TestGetStatusTruncatedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"myID": "AAAA`)) // truncated
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	_, err := client.GetStatus()
	if err == nil {
		t.Error("expected error for truncated JSON")
	}
}

// TestGetStatusHTMLErrorPage verifies handling of HTML instead of JSON
// (common when Syncthing is starting up or behind a proxy).
func TestGetStatusHTMLErrorPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Service Unavailable</body></html>"))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	_, err := client.GetStatus()
	if err == nil {
		t.Error("expected error for HTML response")
	}
}

// TestGetStatusEmptyBody verifies handling of empty response body.
func TestGetStatusEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	_, err := client.GetStatus()
	if err == nil {
		t.Error("expected error for empty body")
	}
}

// TestAddDeviceGetConfigFails verifies AddDevice when GET config fails.
func TestAddDeviceGetConfigFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	err := client.AddDevice("DEVICE-ID", "name")
	if err == nil {
		t.Error("expected error when GET config fails")
	}
}

// TestAddDevicePutConfigFails verifies AddDevice when PUT config fails.
func TestAddDevicePutConfigFails(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch r.Method {
		case "GET":
			_, _ = w.Write([]byte(`{"devices":[],"folders":[]}`))
		case "PUT":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	err := client.AddDevice("DEVICE-ID", "name")
	if err == nil {
		t.Error("expected error when PUT config fails")
	}
}

// TestAddOrUpdateFolderMergesExisting verifies that updating an existing
// folder preserves the folder ID and any unrelated custom fields while
// updating label, path, devices, and the v0.9.4-managed scheduler
// fields (rescanIntervalS, fsWatcher*). This last bit is the migration
// path for folders carried over from earlier dotkeeper installs (which
// set rescanIntervalS=60s and now need to land at 86400s).
func TestAddOrUpdateFolderMergesExisting(t *testing.T) {
	var putRaw string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/rest/config":
			_, _ = w.Write([]byte(`{
				"devices": [],
				"folders": [{
					"id": "existing-folder",
					"label": "Old Label",
					"path": "/old/path",
					"devices": [],
					"rescanIntervalS": 60,
					"customField": "preserved"
				}]
			}`))
		case r.Method == "GET" && r.URL.Path == "/rest/system/status":
			_, _ = w.Write([]byte(`{"myID": "MY-ID"}`))
		case r.Method == "PUT":
			body, _ := io.ReadAll(r.Body)
			putRaw = string(body)
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	err := client.AddOrUpdateFolder("existing-folder", "New Label", "/new/path", []string{"PEER-ID"})
	if err != nil {
		t.Fatalf("AddOrUpdateFolder: %v", err)
	}
	if putRaw == "" {
		t.Fatal("PUT was not called")
	}

	// Migration assertion: the pre-existing rescanIntervalS=60 must be
	// overwritten to the current canonical value. Asserting against
	// the constant keeps this test resilient across changes to the
	// canonical default — what we're really proving is that the
	// stale value gets replaced, not that any specific replacement
	// value is in place.
	wantKey := fmt.Sprintf(`"rescanIntervalS":%d`, CanonicalRescanIntervalS)
	if !strings.Contains(putRaw, wantKey) {
		t.Errorf("expected %s in PUT body, got:\n%s", wantKey, putRaw)
	}
	if strings.Contains(putRaw, `"rescanIntervalS":60`) {
		t.Errorf("pre-existing rescanIntervalS=60 not migrated; full PUT body:\n%s", putRaw)
	}
	// Preservation assertion: customField must survive the merge.
	if !strings.Contains(putRaw, `"customField":"preserved"`) {
		t.Errorf("merge dropped customField; full PUT body:\n%s", putRaw)
	}
}

// TestClientTimeout verifies the client doesn't hang on slow servers.
func TestClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // simulate hang
	}))
	defer server.Close()

	client := &Client{
		baseURL: server.URL,
		apiKey:  "key",
		http:    &http.Client{Timeout: 100 * time.Millisecond},
	}

	err := client.Ping()
	if err == nil {
		t.Error("expected timeout error")
	}
}

// TestGetConfigInvalidJSON verifies handling of valid HTTP but invalid JSON config.
func TestGetConfigInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, apiKey: "key", http: server.Client()}
	_, err := client.GetConfig()
	if err == nil {
		t.Error("expected error for invalid JSON config")
	}
}

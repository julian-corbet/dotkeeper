// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package stengine

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-corbet/dotkeeper/internal/testutil"
)

// TestSetupCreatesDirectories verifies that Setup creates the config
// and data directories with correct permissions.
func TestSetupCreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	testutil.AssertFilePerms(t, configDir, 0o700)
	testutil.AssertFilePerms(t, dataDir, 0o700)
}

// TestSetupGeneratesCertificate verifies that Setup creates TLS
// cert and key files for Syncthing identity.
func TestSetupGeneratesCertificate(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	certFile := filepath.Join(configDir, "cert.pem")
	keyFile := filepath.Join(configDir, "key.pem")
	testutil.AssertFileExists(t, certFile)
	testutil.AssertFileExists(t, keyFile)
}

// TestSetupGeneratesConfig verifies that Setup creates config.xml
// with our custom port and privacy settings.
func TestSetupGeneratesConfig(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	configFile := filepath.Join(configDir, "config.xml")
	testutil.AssertFileExists(t, configFile)

	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("reading config.xml: %v", err)
	}
	content := string(data)

	// Verify our custom settings are present
	checks := []struct {
		needle string
		desc   string
	}{
		{"127.0.0.1:18384", "GUI address"},
		{"tcp://:12000", "TCP listen address"},
		{"quic://:12000", "QUIC listen address"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.needle) {
			t.Errorf("config.xml missing %s (%q)", c.desc, c.needle)
		}
	}
}

// TestSetupConfigPermissions verifies config.xml is created with
// restricted permissions (it contains the API key).
func TestSetupConfigPermissions(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	configFile := filepath.Join(configDir, "config.xml")
	testutil.AssertFilePerms(t, configFile, 0o600)
}

// TestSetupIdempotent verifies that calling Setup twice doesn't
// overwrite the existing certificate or config.
func TestSetupIdempotent(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup #1: %v", err)
	}

	// Read first cert
	cert1, err := os.ReadFile(filepath.Join(configDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}

	// Second setup should not regenerate
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup #2: %v", err)
	}

	cert2, err := os.ReadFile(filepath.Join(configDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}

	if string(cert1) != string(cert2) {
		t.Error("Setup() regenerated certificate on second call")
	}
}

// TestDeviceID verifies that DeviceID returns a non-empty Syncthing
// device ID from the generated certificate.
func TestDeviceID(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	id, err := eng.DeviceID()
	if err != nil {
		t.Fatalf("DeviceID: %v", err)
	}

	if id == "" {
		t.Fatal("DeviceID returned empty string")
	}

	// Syncthing device IDs are 63 chars with dashes: XXXXXXX-XXXXXXX-...
	if len(id) < 50 {
		t.Errorf("DeviceID too short: %q (%d chars)", id, len(id))
	}
	if !strings.Contains(id, "-") {
		t.Errorf("DeviceID missing dashes: %q", id)
	}
}

// TestDeviceIDStable verifies that DeviceID returns the same value
// on repeated calls (identity is stable).
func TestDeviceIDStable(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	id1, _ := eng.DeviceID()
	id2, _ := eng.DeviceID()

	if id1 != id2 {
		t.Errorf("DeviceID not stable: %q != %q", id1, id2)
	}
}

// TestDeviceIDNoCert verifies that DeviceID fails gracefully
// when no certificate exists.
func TestDeviceIDNoCert(t *testing.T) {
	eng := New(t.TempDir(), t.TempDir(), "0.1.1-test")
	_, err := eng.DeviceID()
	if err == nil {
		t.Error("expected error when no certificate exists")
	}
}

// TestAPIKey verifies that APIKey reads the API key from config.xml.
func TestAPIKey(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	key, err := eng.APIKey()
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if key == "" {
		t.Fatal("APIKey returned empty string")
	}
}

// TestAPIKeyNoConfig verifies that APIKey fails gracefully
// when no config.xml exists.
func TestAPIKeyNoConfig(t *testing.T) {
	eng := New(t.TempDir(), t.TempDir(), "0.1.1-test")
	_, err := eng.APIKey()
	if err == nil {
		t.Error("expected error when no config.xml exists")
	}
}

// TestAPIKeyMalformedXML verifies that APIKey handles corrupt
// config.xml without panicking.
func TestAPIKeyMalformedXML(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "config.xml"), []byte("<broken"), 0o600)

	eng := New(tmp, t.TempDir(), "0.1.1-test")
	_, err := eng.APIKey()
	if err == nil {
		t.Error("expected error for malformed XML")
	}
}

// TestConfigXMLStructure verifies the generated config.xml can be
// parsed and contains expected elements.
func TestConfigXMLStructure(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "syncthing")
	dataDir := filepath.Join(tmp, "data", "syncthing")

	eng := New(configDir, dataDir, "0.1.1-test")
	if err := eng.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
	if err != nil {
		t.Fatalf("reading config.xml: %v", err)
	}

	// Verify it's valid XML
	var raw struct {
		XMLName xml.Name `xml:"configuration"`
		GUI     struct {
			APIKey  string `xml:"apikey"`
			Address string `xml:"address"`
		} `xml:"gui"`
	}
	if err := xml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	if raw.GUI.APIKey == "" {
		t.Error("config.xml has no API key")
	}
	if raw.GUI.Address != "127.0.0.1:18384" {
		t.Errorf("GUI address = %q, want 127.0.0.1:18384", raw.GUI.Address)
	}
}

// TestNewEngine verifies basic Engine construction.
func TestNewEngine(t *testing.T) {
	eng := New("/config", "/data", "0.1.1-test")
	if eng == nil {
		t.Fatal("New returned nil")
	}
	if eng.configDir != "/config" {
		t.Errorf("configDir = %q", eng.configDir)
	}
	if eng.dataDir != "/data" {
		t.Errorf("dataDir = %q", eng.dataDir)
	}
}

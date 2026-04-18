// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// Package stclient provides a typed HTTP client for the Syncthing REST API.
package stclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// APIAddress is the loopback address for dotkeeper's isolated Syncthing.
	APIAddress = "127.0.0.1:18384"

	// FolderMarkerName is the marker directory placed at each shared folder
	// root so Syncthing can recognise it. Renamed from the default ".stfolder"
	// to ".dkfolder" to keep user-facing surfaces free of Syncthing branding.
	FolderMarkerName = ".dkfolder"
)

// Client talks to the Syncthing REST API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a REST API client for the embedded Syncthing instance.
func New(apiKey string) *Client {
	return &Client{
		baseURL: "http://" + APIAddress,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Ping checks if Syncthing is responding.
func (c *Client) Ping() error {
	_, err := c.get("rest/system/ping")
	return err
}

// SystemStatus represents the /rest/system/status response.
type SystemStatus struct {
	MyID string `json:"myID"`
}

// GetStatus returns the system status.
func (c *Client) GetStatus() (*SystemStatus, error) {
	data, err := c.get("rest/system/status")
	if err != nil {
		return nil, err
	}
	var status SystemStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// GetConfig returns the full Syncthing configuration as raw JSON.
func (c *Client) GetConfig() (map[string]any, error) {
	data, err := c.get("rest/config")
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SetConfig replaces the full Syncthing configuration.
func (c *Client) SetConfig(cfg map[string]any) error {
	return c.put("rest/config", cfg)
}

// AddDevice adds a device to the Syncthing config if not present.
func (c *Client) AddDevice(deviceID, name string) error {
	cfg, err := c.GetConfig()
	if err != nil {
		return err
	}

	devices, _ := cfg["devices"].([]any)
	for _, d := range devices {
		dm, _ := d.(map[string]any)
		if dm["deviceID"] == deviceID {
			return nil // already present
		}
	}

	newDevice := map[string]any{
		"deviceID":          deviceID,
		"name":              name,
		"addresses":         []string{"dynamic"},
		"compression":       "metadata",
		"introducer":        false,
		"autoAcceptFolders": true,
	}
	devices = append(devices, newDevice)
	cfg["devices"] = devices
	return c.SetConfig(cfg)
}

// AddOrUpdateFolder adds or updates a shared folder.
func (c *Client) AddOrUpdateFolder(id, label, path string, deviceIDs []string) error {
	cfg, err := c.GetConfig()
	if err != nil {
		return err
	}

	// Get my device ID to exclude from folder device list
	status, err := c.GetStatus()
	if err != nil {
		return err
	}

	var folderDevices []map[string]any
	for _, did := range deviceIDs {
		if did != status.MyID && did != "" {
			folderDevices = append(folderDevices, map[string]any{
				"deviceID":     did,
				"introducedBy": "",
			})
		}
	}

	folderCfg := map[string]any{
		"id":               id,
		"label":            label,
		"path":             path,
		"type":             "sendreceive",
		"devices":          folderDevices,
		"rescanIntervalS":  60,
		"fsWatcherEnabled": true,
		"fsWatcherDelayS":  1,
		"ignorePerms":      false,
		"autoNormalize":    true,
		"markerName":       FolderMarkerName,
	}

	folders, _ := cfg["folders"].([]any)
	found := false
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] == id {
			// Merge: update only the fields we manage, preserve user customizations
			fm["label"] = label
			fm["path"] = path
			fm["devices"] = folderDevices
			fm["markerName"] = FolderMarkerName
			folders[i] = fm
			found = true
			break
		}
	}
	if !found {
		folders = append(folders, folderCfg)
	}
	cfg["folders"] = folders
	return c.SetConfig(cfg)
}

func (c *Client) get(endpoint string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/"+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) put(endpoint string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", c.baseURL+"/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("API returned %d", resp.StatusCode)
	}
	return nil
}

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
	"net/url"
	"sync"
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
//
// The client memoizes responses that are expensive to fetch but cheap to
// keep fresh: SystemStatus (immutable for the lifetime of the Syncthing
// process), the raw /rest/config bytes (invalidated on every SetConfig),
// and /rest/system/connections (30 s TTL). Together these cut the steady-
// state HTTP cost of a reconcile tick that adds a device and a folder
// from 3 GET round-trips to 1, with no observable behavioural change for
// callers — every accessor still returns a freshly-unmarshalled value.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client

	mu               sync.Mutex
	cachedConfigJSON []byte
	cachedStatus     *SystemStatus
	cachedConns      *Connections
	cachedConnsAt    time.Time
}

// connectionsCacheTTL bounds how long /rest/system/connections is cached.
// Connection state changes on second-to-minute timescales; 30 s smooths
// bursts of reconciles (e.g. fsnotify-driven) without ever hiding a real
// loss-of-peer event for more than half the reconcile interval.
const connectionsCacheTTL = 30 * time.Second

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

// GetStatus returns the system status. The result is memoized for the
// lifetime of the client: MyID does not change while the Syncthing process
// is running, and SystemStatus exposes no other fields. Callers that need
// a guaranteed-fresh fetch should construct a new Client.
func (c *Client) GetStatus() (*SystemStatus, error) {
	c.mu.Lock()
	if c.cachedStatus != nil {
		s := *c.cachedStatus
		c.mu.Unlock()
		return &s, nil
	}
	c.mu.Unlock()

	data, err := c.get("rest/system/status")
	if err != nil {
		return nil, err
	}
	var status SystemStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}

	c.mu.Lock()
	cp := status
	c.cachedStatus = &cp
	c.mu.Unlock()
	return &status, nil
}

// GetConfig returns the full Syncthing configuration as raw JSON. The
// underlying HTTP response is cached and invalidated whenever SetConfig
// succeeds. Each call returns a freshly-unmarshalled map, so callers may
// mutate the result without disturbing the cache.
func (c *Client) GetConfig() (map[string]any, error) {
	c.mu.Lock()
	cached := c.cachedConfigJSON
	c.mu.Unlock()

	if cached == nil {
		data, err := c.get("rest/config")
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.cachedConfigJSON = data
		cached = data
		c.mu.Unlock()
	}

	var cfg map[string]any
	if err := json.Unmarshal(cached, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SetConfig replaces the full Syncthing configuration and invalidates the
// cached /rest/config response so the next GetConfig fetches fresh state
// rather than the value we just overwrote.
func (c *Client) SetConfig(cfg map[string]any) error {
	if err := c.put("rest/config", cfg); err != nil {
		return err
	}
	c.mu.Lock()
	c.cachedConfigJSON = nil
	c.mu.Unlock()
	return nil
}

// AddDevice adds a device to the Syncthing config if not present, or
// reasserts dotkeeper-owned device fields (currently just
// autoAcceptFolders=false) on the existing entry. Reasserting on every
// call lets a daemon upgrade migrate older installs that were created
// when AddDevice set autoAcceptFolders=true by default.
func (c *Client) AddDevice(deviceID, name string) error {
	cfg, err := c.GetConfig()
	if err != nil {
		return err
	}

	devices, _ := cfg["devices"].([]any)
	for i, d := range devices {
		dm, _ := d.(map[string]any)
		if dm["deviceID"] == deviceID {
			if v, _ := dm["autoAcceptFolders"].(bool); v {
				dm["autoAcceptFolders"] = false
				devices[i] = dm
				cfg["devices"] = devices
				return c.SetConfig(cfg)
			}
			return nil
		}
	}

	newDevice := map[string]any{
		"deviceID":    deviceID,
		"name":        name,
		"addresses":   []string{"dynamic"},
		"compression": "metadata",
		"introducer":  false,
		// Folder membership is opt-in per machine: each receiver
		// decides which offered folders to take. Auto-accept would
		// also require a sane default folder path template — without
		// one, Syncthing logs ERROR "Failed to auto-accept folder
		// due to path conflict" on every ClusterConfig from a peer
		// announcing folders we don't have, which is the default
		// state of any partial-overlap fleet (machine A has repos
		// X+Y, machine B has Y+Z — both peers will offer the
		// non-shared half forever). Keeping this false eliminates
		// that storm at the source.
		"autoAcceptFolders": false,
	}
	devices = append(devices, newDevice)
	cfg["devices"] = devices
	return c.SetConfig(cfg)
}

// MigrateDisableAutoAcceptFolders walks every device in the current
// Syncthing config and clears the autoAcceptFolders flag if set. Used
// at daemon startup to migrate installs created before the default
// flipped to false in v1.1.14. Returns the count of devices migrated
// so callers can log a one-line summary.
//
// Idempotent: a no-op once every device is already false. The single
// GetConfig + (optional) SetConfig round trip costs ~milliseconds at
// startup and saves the ERROR-storm cost for the life of the daemon.
func (c *Client) MigrateDisableAutoAcceptFolders() (int, error) {
	cfg, err := c.GetConfig()
	if err != nil {
		return 0, err
	}
	devices, _ := cfg["devices"].([]any)
	migrated := 0
	for i, d := range devices {
		dm, _ := d.(map[string]any)
		if v, _ := dm["autoAcceptFolders"].(bool); v {
			dm["autoAcceptFolders"] = false
			devices[i] = dm
			migrated++
		}
	}
	if migrated == 0 {
		return 0, nil
	}
	cfg["devices"] = devices
	if err := c.SetConfig(cfg); err != nil {
		return 0, err
	}
	return migrated, nil
}

// Connection represents one entry of /rest/system/connections.
// Only the fields dotkeeper actually needs are decoded; the Syncthing
// REST payload contains many more that we deliberately ignore.
type Connection struct {
	Connected     bool   `json:"connected"`
	Address       string `json:"address"`
	ClientVersion string `json:"clientVersion"`
}

// Connections is the shape of /rest/system/connections — a map of
// deviceID → Connection, plus a "total" entry Syncthing also returns.
// The total entry is irrelevant for per-peer reachability, so callers
// are expected to filter by their known device IDs.
type Connections struct {
	Connections map[string]Connection `json:"connections"`
}

// GetConnections queries /rest/system/connections. The result is cached
// for connectionsCacheTTL to smooth bursts of rapid reconciles (fsnotify-
// driven, for instance) without paying an HTTP round-trip and a JSON parse
// of the whole peer table each time.
func (c *Client) GetConnections() (*Connections, error) {
	c.mu.Lock()
	if c.cachedConns != nil && time.Since(c.cachedConnsAt) < connectionsCacheTTL {
		conns := *c.cachedConns
		c.mu.Unlock()
		return &conns, nil
	}
	c.mu.Unlock()

	data, err := c.get("rest/system/connections")
	if err != nil {
		return nil, err
	}
	var conns Connections
	if err := json.Unmarshal(data, &conns); err != nil {
		return nil, err
	}

	c.mu.Lock()
	cp := conns
	c.cachedConns = &cp
	c.cachedConnsAt = time.Now()
	c.mu.Unlock()
	return &conns, nil
}

// FolderStatus represents one /rest/db/status response. Syncthing
// reports the current lifecycle state (idle, syncing, scanning, error,
// stopped, …) plus detailed counts we don't currently use.
type FolderStatus struct {
	State     string `json:"state"`
	Errors    int    `json:"errors"`
	NeedFiles int    `json:"needFiles"`
}

// GetFolderStatus queries /rest/db/status?folder=<id> for one folder.
func (c *Client) GetFolderStatus(folderID string) (*FolderStatus, error) {
	data, err := c.get("rest/db/status?folder=" + folderID)
	if err != nil {
		return nil, err
	}
	var s FolderStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Canonical scheduler defaults written by dotkeeper into every managed
// Syncthing folder. Centralised so the diff (which detects drift) and
// the applier (which writes the desired values) share one definition.
//
// rescanIntervalS=0 (Syncthing's "fsWatcher only, no periodic rescan"
// mode). dotkeeper drives rescans reactively via the watchhealth
// package: when an event-queue overflow, watch-limit hit,
// suspend/resume, or untrusted-filesystem condition is detected, the
// diff emits a RescanFolderNow action that calls
// POST /rest/db/scan?folder=ID. A weekly backstop rescan covers
// detector blind spots.
//
// Migration history of this constant:
//
//   - pre-v0.9.4: 60 (one full tree walk per minute, dominated CPU)
//   - v0.9.4: 86400 (daily safety-net rescan; covered most failure
//     modes but burned CPU on healthy hosts)
//   - v0.9.7: 0 (dotkeeper-driven; see internal/watchhealth)
//
// The drift detector in internal/reconcile actively migrates folders
// carried over from any prior value on first reconcile after
// upgrade, so the constant change here is the only update needed.
const (
	CanonicalRescanIntervalS  = 0
	CanonicalFsWatcherEnabled = true
	CanonicalFsWatcherDelayS  = 1
)

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
		"id":      id,
		"label":   label,
		"path":    path,
		"type":    "sendreceive",
		"devices": folderDevices,
		// Daily full rescan. fsWatcher (inotify on Linux,
		// FSEvents on macOS, ReadDirectoryChangesW on Windows)
		// catches every real-time change; the periodic rescan is
		// the belt-and-suspenders safety net for the rare case
		// where the OS event API drops events under extreme
		// kernel pressure. The prior default (60s, one full rescan
		// per minute per folder) was a defensive holdover from
		// early dotkeeper builds before fsWatcher was trusted; it
		// dominated daemon CPU on fleets with many folders without
		// any operational benefit because the per-minute walk
		// duplicated the inotify signal.
		"rescanIntervalS":  CanonicalRescanIntervalS,
		"fsWatcherEnabled": CanonicalFsWatcherEnabled,
		"fsWatcherDelayS":  CanonicalFsWatcherDelayS,
		"ignorePerms":      false,
		"autoNormalize":    true,
		"markerName":       FolderMarkerName,
	}

	folders, _ := cfg["folders"].([]any)
	found := false
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] == id {
			// Merge: update fields we manage, preserve user customizations.
			// rescanIntervalS is now in the managed set so the v0.9.4
			// migration applies to folders carried over from earlier
			// dotkeeper installs (which set 60s). If a user wants a
			// different interval per-folder, the right place is an
			// override knob in .dotkeeper.toml (not implemented in
			// v0.9.4 — pending genuine demand).
			fm["label"] = label
			fm["path"] = path
			fm["devices"] = folderDevices
			fm["markerName"] = FolderMarkerName
			fm["rescanIntervalS"] = CanonicalRescanIntervalS
			fm["fsWatcherEnabled"] = CanonicalFsWatcherEnabled
			fm["fsWatcherDelayS"] = CanonicalFsWatcherDelayS
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("API returned %d", resp.StatusCode)
	}
	return nil
}

// ScheduleRescan requests an immediate full rescan of the named
// folder via Syncthing's POST /rest/db/scan?folder=ID endpoint.
// Used by the v0.9.7 watchhealth-driven rescheduler: when the
// filesystem-event API is suspected to have missed something
// (queue overflow, suspend/resume, untrusted FS hit a backstop
// interval), reconcile emits a RescanFolderNow action that calls
// this method.
//
// The Syncthing endpoint returns 200 with no body on success or
// 500 with an error message on failure (e.g. folder ID unknown,
// folder currently paused). We surface the 500 body verbatim so
// operators get an actionable error.
func (c *Client) ScheduleRescan(folderID string) error {
	if folderID == "" {
		return fmt.Errorf("ScheduleRescan: empty folder ID")
	}
	// /rest/db/scan accepts ?folder=<id> with no body. The endpoint
	// is a POST per the Syncthing REST docs; SetConfig elsewhere
	// uses PUT, so we inline the request rather than introduce a
	// generic post helper used by exactly one call site.
	endpoint := url.PathEscape(folderID)
	req, err := http.NewRequest("POST", c.baseURL+"/rest/db/scan?folder="+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ScheduleRescan %q: HTTP %d: %s", folderID, resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

// SetFolderPaused sets the paused flag on an existing Syncthing folder
// to the given value. Pausing stops the folder's scanner, fsWatcher,
// and BEP gossip; the folder's index DB stays on disk but is unloaded
// from memory. Unpausing is the inverse. Returns an error if the
// folder ID is not present in Syncthing's config.
//
// Used by the v0.9.6 auto-pause feature: reconcile pauses folders
// that have been quiet on the local filesystem for longer than the
// idle threshold, and unpauses immediately when activity reappears.
func (c *Client) SetFolderPaused(folderID string, paused bool) error {
	cfg, err := c.GetConfig()
	if err != nil {
		return err
	}
	folders, _ := cfg["folders"].([]any)
	found := false
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] == folderID {
			fm["paused"] = paused
			folders[i] = fm
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("folder %q not found in Syncthing config", folderID)
	}
	cfg["folders"] = folders
	return c.SetConfig(cfg)
}

// UpdateFolderSchedule rewrites the scheduler fields (rescanIntervalS,
// fsWatcherEnabled, fsWatcherDelayS) on an existing folder to the
// canonical dotkeeper-managed values, leaving all other fields
// (devices, label, custom user fields) untouched. Returns an error if
// the folder ID is not present in Syncthing's config.
//
// This exists separately from AddOrUpdateFolder because reconcile's
// drift detector emits UpdateSyncthingFolderSchedule when only the
// scheduler fields are wrong — in which case calling AddOrUpdateFolder
// would also rewrite devices/label/path needlessly.
func (c *Client) UpdateFolderSchedule(folderID string) error {
	cfg, err := c.GetConfig()
	if err != nil {
		return err
	}
	folders, _ := cfg["folders"].([]any)
	found := false
	for i, f := range folders {
		fm, _ := f.(map[string]any)
		if fm["id"] == folderID {
			fm["rescanIntervalS"] = CanonicalRescanIntervalS
			fm["fsWatcherEnabled"] = CanonicalFsWatcherEnabled
			fm["fsWatcherDelayS"] = CanonicalFsWatcherDelayS
			folders[i] = fm
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("folder %q not found in Syncthing config", folderID)
	}
	cfg["folders"] = folders
	return c.SetConfig(cfg)
}

// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

package reconcile

import (
	"os"
	"sync"
	"time"

	"github.com/julian-corbet/dotkeeper/internal/config"
)

// configCache memoizes parsed TOML config values across reconcile ticks. The
// freshness key is (mtime, size) of the on-disk file: a reparse only happens
// when the file actually changed. Without this cache, NewDesiredProvider's
// closure re-reads machine.toml, state.toml, and every .dotkeeper.toml under
// the scan roots every reconcile tick (5min by default), even though those
// files change on a human timescale, not a machine one.
//
// The cache lives for the lifetime of the provider closure and is therefore
// per-process. There is no time-based eviction: stale entries cost O(filesize)
// memory each, which is negligible (KB-class TOML files) and not worth the
// complexity of a TTL. A removed file naturally falls out of the cache map
// because its key no longer appears in any reconcile pass — but it stays in
// the map until process exit. For dotkeeper's typical scan footprint (≤100
// repos) this is < 1 MB resident, which we trade gladly for correctness.
type configCache struct {
	mu      sync.Mutex
	machine map[string]*machineEntry
	state   map[string]*stateEntry
	repo    map[string]*repoEntry
}

type machineEntry struct {
	mtime time.Time
	size  int64
	value *config.MachineConfigV2
}

type stateEntry struct {
	mtime   time.Time
	size    int64
	value   *config.StateV2
	tracked []string
	// notFound is true when a stat showed the file does not exist; we cache
	// the absence so we don't re-stat every tick during first-run.
	notFound bool
}

type repoEntry struct {
	mtime time.Time
	size  int64
	value *config.RepoConfigV2
}

// statKey returns (mtime, size) for path. If the file does not exist, it
// returns ok=false; callers treat that as a cache miss.
func statKey(path string) (mtime time.Time, size int64, exists bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, false
	}
	return info.ModTime(), info.Size(), true
}

func (c *configCache) loadMachine(path string) (*config.MachineConfigV2, error) {
	mtime, size, exists := statKey(path)
	if !exists {
		// Reproduce the original error message verbatim.
		return loadMachineConfigFromPath(path)
	}
	c.mu.Lock()
	if c.machine == nil {
		c.machine = make(map[string]*machineEntry)
	}
	if e, ok := c.machine[path]; ok && e.mtime.Equal(mtime) && e.size == size {
		c.mu.Unlock()
		return e.value, nil
	}
	c.mu.Unlock()

	v, err := loadMachineConfigFromPath(path)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.machine[path] = &machineEntry{mtime: mtime, size: size, value: v}
	c.mu.Unlock()
	return v, nil
}

func (c *configCache) loadState(path string) (*config.StateV2, []string, error) {
	mtime, size, exists := statKey(path)

	c.mu.Lock()
	if c.state == nil {
		c.state = make(map[string]*stateEntry)
	}
	e, ok := c.state[path]
	c.mu.Unlock()

	if !exists {
		if ok && e.notFound {
			return nil, nil, nil
		}
		v, tracked, err := loadStateFromPath(path)
		if err != nil {
			return nil, nil, err
		}
		c.mu.Lock()
		c.state[path] = &stateEntry{notFound: true, value: v, tracked: tracked}
		c.mu.Unlock()
		return v, tracked, nil
	}

	if ok && !e.notFound && e.mtime.Equal(mtime) && e.size == size {
		return e.value, e.tracked, nil
	}

	v, tracked, err := loadStateFromPath(path)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.state[path] = &stateEntry{mtime: mtime, size: size, value: v, tracked: tracked}
	c.mu.Unlock()
	return v, tracked, nil
}

func (c *configCache) loadRepo(repoDir string) (*config.RepoConfigV2, error) {
	markerPath := config.RepoConfigPath(repoDir)
	mtime, size, exists := statKey(markerPath)
	if !exists {
		// No marker means LoadRepoConfigV2 returns (nil, nil); skip the cache.
		return config.LoadRepoConfigV2(repoDir)
	}
	c.mu.Lock()
	if c.repo == nil {
		c.repo = make(map[string]*repoEntry)
	}
	if e, ok := c.repo[repoDir]; ok && e.mtime.Equal(mtime) && e.size == size {
		c.mu.Unlock()
		return e.value, nil
	}
	c.mu.Unlock()

	v, err := config.LoadRepoConfigV2(repoDir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.repo[repoDir] = &repoEntry{mtime: mtime, size: size, value: v}
	c.mu.Unlock()
	return v, nil
}

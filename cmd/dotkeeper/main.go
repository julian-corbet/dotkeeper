// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only

// dotkeeper — P2P repo sync with git history.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/julian-corbet/dotkeeper/internal/config"
	"github.com/julian-corbet/dotkeeper/internal/gitsync"
	"github.com/julian-corbet/dotkeeper/internal/service"
	"github.com/julian-corbet/dotkeeper/internal/stclient"
	"github.com/julian-corbet/dotkeeper/internal/stengine"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	root := &cobra.Command{
		Use:   "dotkeeper",
		Short: "P2P repo sync with git history",
		Long:  "Embeds Syncthing for real-time file sync between machines.\nUses git for history and backup.",
	}

	root.AddCommand(versionCmd())
	root.AddCommand(initCmd())
	root.AddCommand(joinCmd())
	root.AddCommand(addCmd())
	root.AddCommand(removeCmd())
	root.AddCommand(pairCmd())
	root.AddCommand(syncCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(installTimerCmd())
	root.AddCommand(startCmd())
	root.AddCommand(stopCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- Helpers ---

func engine() *stengine.Engine {
	return stengine.New(config.STConfigDir(), config.STDataDir())
}

func apiClient() *stclient.Client {
	eng := engine()
	key, err := eng.APIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: cannot read API key: %v\n", err)
		os.Exit(1)
	}
	return stclient.New(key)
}

func truncateID(id string) string {
	if len(id) >= 8 {
		return id[:8] + "..."
	}
	return id
}

func machineKey(name string) string {
	key := strings.ToLower(name)
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, ".", "_")
	key = strings.ReplaceAll(key, " ", "_")
	return key
}

func svcManager() service.Manager {
	mgr, err := service.Detect()
	if err != nil || mgr == nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: service detection failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "[dotkeeper] falling back to manual mode — start Syncthing with: dotkeeper start")
		// Return a no-op manager that won't panic
		return &service.NoopManager{}
	}
	return mgr
}

func binaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "dotkeeper"
	}
	return exe
}

func writeConfigStignore() error {
	content := "// Managed by dotkeeper — machine.toml is local identity, never synced\nmachine.toml\n"
	return os.WriteFile(filepath.Join(config.ConfigDir(), ".stignore"), []byte(content), 0o644)
}

func writeStignore(dir string, patterns []string) error {
	content := "// Managed by dotkeeper — regenerated on 'dotkeeper add' and 'dotkeeper pair'\n"
	for _, p := range patterns {
		content += p + "\n"
	}
	return os.WriteFile(filepath.Join(dir, ".stignore"), []byte(content), 0o644)
}

func requireMachine() *config.MachineConfig {
	m, err := config.LoadMachineConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
		os.Exit(1)
	}
	if m == nil {
		fmt.Fprintln(os.Stderr, "[dotkeeper] ERROR: not initialized — run 'dotkeeper init' first")
		os.Exit(1)
	}
	return m
}

func requireConfig() *config.SharedConfig {
	cfg, err := config.LoadSharedConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "[dotkeeper] ERROR: config.toml not found — run 'dotkeeper init' first")
		os.Exit(1)
	}
	return cfg
}

// configureAllFolders configures Syncthing folders for all repos in the config.
func configureAllFolders(client *stclient.Client, cfg *config.SharedConfig) {
	var deviceIDs []string
	for _, m := range cfg.Machines {
		if m.SyncthingID != "" {
			deviceIDs = append(deviceIDs, m.SyncthingID)
		}
	}

	for _, repo := range cfg.Repos {
		path := config.ExpandPath(repo.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("[dotkeeper] WARNING: %s: path %s does not exist, skipping\n", repo.Name, path)
			continue
		}
		fid := "dotkeeper-" + repo.Name
		if err := client.AddOrUpdateFolder(fid, repo.Name, path, deviceIDs); err != nil {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: folder %s: %v\n", repo.Name, err)
		}
		if err := writeStignore(path, cfg.Syncthing.Ignore); err != nil {
			fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: .stignore in %s: %v\n", repo.Name, err)
		}
	}
}

// --- Commands ---

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print dotkeeper version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("dotkeeper %s (%s)\n", version, commit)
		},
	}
}

func initCmd() *cobra.Command {
	var name string
	var slot int
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize this machine for dotkeeper",
		Long:  "Creates machine identity, generates Syncthing keys, installs services,\nand creates the shared config. Run this on the first machine.",
		Run: func(cmd *cobra.Command, args []string) {
			existing, _ := config.LoadMachineConfig()
			if existing != nil && !force {
				fmt.Printf("[dotkeeper] already initialized as '%s' (slot %d)\n", existing.Name, existing.Slot)
				eng := engine()
				if id, err := eng.DeviceID(); err == nil {
					fmt.Printf("[dotkeeper] device ID: %s\n", id)
				}
				fmt.Println("[dotkeeper] use --force to reinitialize")
				return
			}

			if name == "" {
				hostname, _ := os.Hostname()
				name = hostname
			}
			if slot < 0 {
				slot = 0
			}

			// 1. Machine identity
			if err := config.WriteMachineConfig(name, slot); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[dotkeeper] machine: %s (slot %d)\n", name, slot)

			// 2. Syncthing identity
			eng := engine()
			if err := eng.Setup(); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			deviceID, err := eng.DeviceID()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[dotkeeper] Syncthing identity generated\n")

			// 3. Shared config
			cfg, _ := config.LoadSharedConfig()
			if cfg == nil {
				cfg = &config.SharedConfig{
					Sync:      config.SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
					Syncthing: config.SyncthingConfig{Ignore: config.DefaultIgnorePatterns()},
					Machines:  make(map[string]config.MachineEntry),
				}
			}
			config.AddMachine(cfg, machineKey(name), name, slot, deviceID)
			if err := config.WriteSharedConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: writing config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[dotkeeper] wrote %s\n", config.SharedConfigPath())

			// 4. Syncthing service
			mgr := svcManager()
			binPath := binaryPath()
			if err := mgr.InstallSyncthing(binPath); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: service install: %v\n", err)
			} else {
				fmt.Printf("[dotkeeper] started Syncthing service (%s)\n", service.PlatformName(mgr))
			}

			// 5. Configure the config directory as a Syncthing folder
			time.Sleep(2 * time.Second) // give Syncthing a moment to start
			client := apiClient()
			if err := client.Ping(); err == nil {
				if err := client.AddOrUpdateFolder("dotkeeper-config", "dotkeeper-config", config.ConfigDir(), nil); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: config folder: %v\n", err)
				}
				if err := writeConfigStignore(); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: .stignore: %v\n", err)
				}
			}

			// 6. Print result
			fmt.Println()
			fmt.Printf("[dotkeeper] device ID: %s\n", deviceID)
			fmt.Println()
			fmt.Println("[dotkeeper] to add another machine, run on it:")
			fmt.Printf("[dotkeeper]   dotkeeper join %s\n", deviceID)
			fmt.Println()
			fmt.Println("[dotkeeper] to add repos:")
			fmt.Println("[dotkeeper]   dotkeeper add ~/path/to/repo")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "machine name (default: hostname)")
	cmd.Flags().IntVar(&slot, "slot", -1, "timer slot (default: 0)")
	cmd.Flags().BoolVar(&force, "force", false, "reinitialize")
	return cmd
}

func joinCmd() *cobra.Command {
	var name string
	var slot int

	cmd := &cobra.Command{
		Use:   "join <DEVICE-ID>",
		Short: "Join an existing dotkeeper setup",
		Long:  "Initializes this machine and pairs it with an existing dotkeeper machine.\nPass the device ID shown by 'dotkeeper init' on the first machine.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			peerDeviceID := args[0]
			if len(peerDeviceID) < 20 {
				fmt.Fprintln(os.Stderr, "[dotkeeper] ERROR: invalid device ID")
				fmt.Fprintln(os.Stderr, "[dotkeeper]   device IDs look like: XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX")
				os.Exit(1)
			}

			// Check if already initialized — if so, just add the peer
			existing, _ := config.LoadMachineConfig()
			if existing != nil {
				fmt.Printf("[dotkeeper] already initialized as '%s' (slot %d)\n", existing.Name, existing.Slot)
				name = existing.Name
				slot = existing.Slot
			} else {
				// First-time init
				if name == "" {
					hostname, _ := os.Hostname()
					name = hostname
				}
				if slot < 0 {
					cfg, _ := config.LoadSharedConfig()
					if cfg != nil {
						slot = len(cfg.Machines)
					} else {
						slot = 1
					}
				}

				if err := config.WriteMachineConfig(name, slot); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("[dotkeeper] machine: %s (slot %d)\n", name, slot)

				eng := engine()
				if err := eng.Setup(); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
					os.Exit(1)
				}
				fmt.Println("[dotkeeper] Syncthing identity generated")

				mgr := svcManager()
				binPath := binaryPath()
				if err := mgr.InstallSyncthing(binPath); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: service install: %v\n", err)
				} else {
					fmt.Printf("[dotkeeper] started Syncthing service (%s)\n", service.PlatformName(mgr))
				}
			}

			deviceID, err := engine().DeviceID()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}

			// Wait for Syncthing API
			fmt.Println("[dotkeeper] waiting for Syncthing...")
			client := apiClient()
			for i := 0; i < 30; i++ {
				if err := client.Ping(); err == nil {
					break
				}
				time.Sleep(time.Second)
			}

			// Add peer device
			fmt.Printf("[dotkeeper] adding peer device (%s)\n", truncateID(peerDeviceID))
			if err := client.AddDevice(peerDeviceID, "peer"); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: add device: %v\n", err)
			}

			// 6. Share config directory with peer
			if err := client.AddOrUpdateFolder("dotkeeper-config", "dotkeeper-config", config.ConfigDir(), []string{peerDeviceID, deviceID}); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: config folder: %v\n", err)
			}
			if err := writeConfigStignore(); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: .stignore: %v\n", err)
			}

			// 7. Wait for config.toml to arrive from peer
			fmt.Println("[dotkeeper] waiting for config from peer... (timeout: 120s)")
			configArrived := false
			for i := 0; i < 120; i++ {
				if _, err := os.Stat(config.SharedConfigPath()); err == nil {
					configArrived = true
					break
				}
				time.Sleep(time.Second)
				if i > 0 && i%15 == 0 {
					fmt.Printf("[dotkeeper]   still waiting... (%ds)\n", i)
				}
			}

			if configArrived {
				fmt.Println("[dotkeeper] received config from peer")
			} else {
				fmt.Println("[dotkeeper] config not received yet — Syncthing will keep trying in background")
				fmt.Println("[dotkeeper] the peer may need to accept this device")
				fmt.Println("[dotkeeper] once connected, run: dotkeeper pair")
			}

			// 8. Register this machine in the shared config
			cfg, _ := config.LoadSharedConfig()
			if cfg == nil {
				cfg = &config.SharedConfig{
					Sync:      config.SyncConfig{GitInterval: "daily", SlotOffsetMinutes: 5},
					Syncthing: config.SyncthingConfig{Ignore: config.DefaultIgnorePatterns()},
					Machines:  make(map[string]config.MachineEntry),
				}
			}
			config.AddMachine(cfg, machineKey(name), name, slot, deviceID)
			if err := config.WriteSharedConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: writing config: %v\n", err)
			}
			fmt.Printf("[dotkeeper] registered '%s' in config\n", name)

			// 9. Apply config — add all devices and folders
			for _, m := range cfg.Machines {
				if m.SyncthingID != "" && m.SyncthingID != deviceID {
					if err := client.AddDevice(m.SyncthingID, m.Hostname); err != nil {
						fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: add device %s: %v\n", m.Hostname, err)
					}
				}
			}
			configureAllFolders(client, cfg)

			// 10. Print result
			fmt.Println()
			fmt.Printf("[dotkeeper] device ID: %s\n", deviceID)
			fmt.Println()
			if configArrived {
				fmt.Printf("[dotkeeper] joined successfully. %d repo(s) configured.\n", len(cfg.Repos))
				fmt.Println("[dotkeeper] add repos with: dotkeeper add ~/path/to/repo")
			}
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "machine name (default: hostname)")
	cmd.Flags().IntVar(&slot, "slot", -1, "timer slot (default: auto)")
	return cmd
}

func addCmd() *cobra.Command {
	var noGit bool
	var repoName string

	cmd := &cobra.Command{
		Use:   "add <path> [path...]",
		Short: "Add repos to sync",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			machine := requireMachine()
			cfg := requireConfig()
			client := apiClient()

			var deviceIDs []string
			for _, m := range cfg.Machines {
				if m.SyncthingID != "" {
					deviceIDs = append(deviceIDs, m.SyncthingID)
				}
			}

			for _, rawPath := range args {
				path := config.ExpandPath(rawPath)
				absPath, _ := filepath.Abs(path)

				if _, err := os.Stat(absPath); os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %s does not exist\n", rawPath)
					continue
				}

				name := repoName
				if name == "" {
					name = filepath.Base(absPath)
				}

				// Check if already tracked
				alreadyTracked := false
				for _, r := range cfg.Repos {
					if r.Name == name {
						alreadyTracked = true
						break
					}
				}
				if alreadyTracked {
					fmt.Printf("[dotkeeper] '%s' is already tracked\n", name)
					continue
				}

				// Detect git
				hasGit := false
				if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
					hasGit = true
				}
				useGit := hasGit && !noGit

				// Add to shared config
				displayPath := config.ContractPath(absPath)
				if config.AddRepo(cfg, name, displayPath, useGit) {
					fmt.Printf("[dotkeeper] added: %s → %s", name, displayPath)
					if useGit {
						fmt.Print(" (git: yes)")
					}
					fmt.Println()
				}

				// Write .stignore
				if err := writeStignore(absPath, cfg.Syncthing.Ignore); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: .stignore: %v\n", err)
				}

				// Configure Syncthing folder
				fid := "dotkeeper-" + name
				if err := client.AddOrUpdateFolder(fid, name, absPath, deviceIDs); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: syncthing folder: %v\n", err)
				}

				// Create per-repo dotkeeper.toml
				if err := config.CreateRepoLog(absPath, name, machineKey(machine.Name)); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: repo log: %v\n", err)
				}
			}

			// Save updated config
			if err := config.WriteSharedConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}

			fmt.Println("[dotkeeper] config updated — changes will sync to other machines automatically")
		},
	}
	cmd.Flags().BoolVar(&noGit, "no-git", false, "sync via Syncthing only, no git auto-commit")
	cmd.Flags().StringVar(&repoName, "name", "", "override the repo name (default: directory name)")
	return cmd
}

func removeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a repo from sync",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := requireConfig()
			name := args[0]

			if !config.RemoveRepo(cfg, name) {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: repo '%s' not found\n", name)
				os.Exit(1)
			}

			if err := config.WriteSharedConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("[dotkeeper] removed '%s' from config\n", name)
			fmt.Println("[dotkeeper] note: files are untouched, only sync is stopped")
		},
	}
}

func pairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Apply config — add all devices and folders to Syncthing",
		Run: func(cmd *cobra.Command, args []string) {
			_ = requireMachine()
			cfg := requireConfig()
			client := apiClient()

			if err := client.Ping(); err != nil {
				fmt.Fprintln(os.Stderr, "[dotkeeper] ERROR: Syncthing not running")
				fmt.Fprintln(os.Stderr, "[dotkeeper]   run: dotkeeper init (or restart the service)")
				os.Exit(1)
			}

			status, _ := client.GetStatus()
			myID := ""
			if status != nil {
				myID = status.MyID
			}

			for _, m := range cfg.Machines {
				if m.SyncthingID != "" && m.SyncthingID != myID {
					fmt.Printf("[dotkeeper] adding device: %s (%s)\n", m.Hostname, truncateID(m.SyncthingID))
					if err := client.AddDevice(m.SyncthingID, m.Hostname); err != nil {
						fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: add device %s: %v\n", m.Hostname, err)
					}
				}
			}

			// Config directory itself is always synced
			var allIDs []string
			for _, m := range cfg.Machines {
				if m.SyncthingID != "" {
					allIDs = append(allIDs, m.SyncthingID)
				}
			}
			if err := client.AddOrUpdateFolder("dotkeeper-config", "dotkeeper-config", config.ConfigDir(), allIDs); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: config folder: %v\n", err)
			}
			if err := writeConfigStignore(); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: .stignore: %v\n", err)
			}

			configureAllFolders(client, cfg)
			fmt.Println("[dotkeeper] pairing complete")
		},
	}
}

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Run git backup on all configured repos",
		Run: func(cmd *cobra.Command, args []string) {
			machine := requireMachine()
			cfg := requireConfig()

			var gitRepos []config.RepoEntry
			for _, r := range cfg.Repos {
				if r.Git {
					gitRepos = append(gitRepos, r)
				}
			}

			fmt.Printf("[dotkeeper] git backup (%s, %d repos)\n", machine.Name, len(gitRepos))
			for _, repo := range gitRepos {
				path := config.ExpandPath(repo.Path)

				// Update per-repo log
				config.TouchRepoLog(path, machineKey(machine.Name))

				if err := gitsync.SyncRepo(path, machine.Name); err != nil {
					fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: %s: %v\n", repo.Name, err)
				}
			}
			fmt.Println("[dotkeeper] git backup complete")
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show full status",
		Run: func(cmd *cobra.Command, args []string) {
			machine, _ := config.LoadMachineConfig()
			cfg, _ := config.LoadSharedConfig()

			fmt.Println("=== Machine ===")
			if machine != nil {
				fmt.Printf("  Name: %s\n", machine.Name)
				fmt.Printf("  Slot: %d\n", machine.Slot)
			} else {
				fmt.Println("  Not initialized (run 'dotkeeper init')")
			}

			fmt.Printf("\n=== Syncthing (embedded, API %s) ===\n", stclient.APIAddress)
			eng := engine()
			deviceID, _ := eng.DeviceID()

			mgr := svcManager()
			if mgr.IsSyncthingRunning() {
				fmt.Println("  Status: running")
				if deviceID != "" {
					fmt.Printf("  Device ID: %s\n", deviceID)
				}
				fmt.Printf("  Sync port: 12000\n")

				client := apiClient()
				if apiCfg, err := client.GetConfig(); err == nil {
					devices, _ := apiCfg["devices"].([]any)
					folders, _ := apiCfg["folders"].([]any)
					peerCount := 0
					for _, d := range devices {
						dm, _ := d.(map[string]any)
						did, _ := dm["deviceID"].(string)
						if did != deviceID {
							peerCount++
							dname, _ := dm["name"].(string)
							fmt.Printf("  Peer: %s (%s)\n", dname, truncateID(did))
						}
					}
					if peerCount == 0 {
						fmt.Println("  Peers: none")
					}
					fmt.Printf("  Folders: %d\n", len(folders))
				}
			} else {
				fmt.Println("  Status: not running")
				if deviceID != "" {
					fmt.Printf("  Device ID: %s\n", deviceID)
				}
			}

			if cfg != nil {
				fmt.Println("\n=== Machines ===")
				for name, m := range cfg.Machines {
					fmt.Printf("  %s: %s (slot %d)\n", name, m.Hostname, m.Slot)
				}

				fmt.Println("\n=== Repos ===")
				for _, repo := range cfg.Repos {
					path := config.ExpandPath(repo.Path)
					status := "ok"
					if _, err := os.Stat(path); os.IsNotExist(err) {
						status = "MISSING"
					}
					flags := []string{}
					if repo.Git {
						flags = append(flags, "git")
					}
					if _, err := os.Stat(filepath.Join(path, ".stignore")); err == nil {
						flags = append(flags, "syncthing")
					}
					if _, err := os.Stat(filepath.Join(path, "dotkeeper.toml")); err == nil {
						flags = append(flags, "logged")
					}
					fmt.Printf("  %s: %s [%s, %s]\n", repo.Name, repo.Path, status, strings.Join(flags, ", "))
				}
			}

			fmt.Println("\n=== Git Backup Timer ===")
			if mgr.IsTimerActive() {
				fmt.Println("  Status: active")
			} else {
				fmt.Println("  Status: inactive — run: dotkeeper install-timer")
			}
		},
	}
}

func installTimerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-timer",
		Short: "Install scheduled git backup",
		Run: func(cmd *cobra.Command, args []string) {
			machine := requireMachine()
			cfg := requireConfig()

			slot := machine.Slot
			offset := slot * cfg.Sync.SlotOffsetMinutes
			interval := cfg.Sync.GitInterval

			// Build a systemd-style OnCalendar string — platform backends
			// convert this to their native format (launchd, cron, schtasks)
			var onCalendar string
			switch {
			case interval == "hourly":
				onCalendar = fmt.Sprintf("*:%02d", offset)
			case interval == "daily":
				onCalendar = fmt.Sprintf("*-*-* 02:%02d:00", offset)
			case interval == "weekly":
				onCalendar = fmt.Sprintf("Mon 02:%02d:00", offset)
			case interval == "monthly":
				onCalendar = fmt.Sprintf("*-*-01 02:%02d:00", offset)
			case strings.HasSuffix(interval, "h"):
				hours := strings.TrimSuffix(interval, "h")
				onCalendar = fmt.Sprintf("0/%s:%02d", hours, offset)
			default:
				onCalendar = interval
			}

			mgr := svcManager()
			binPath := binaryPath()
			if err := mgr.InstallTimer(binPath, config.SharedConfigPath(), onCalendar); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("[dotkeeper] timer installed (%s, slot %d, %s)\n", interval, slot, service.PlatformName(mgr))
		},
	}
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start embedded Syncthing (foreground, for systemd)",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			eng := engine()
			if err := eng.Start(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] ERROR: %v\n", err)
				os.Exit(1)
			}
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Syncthing service",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("[dotkeeper] stopping Syncthing...")
			mgr := svcManager()
			if err := mgr.StopSyncthing(); err != nil {
				fmt.Fprintf(os.Stderr, "[dotkeeper] WARNING: %v\n", err)
			}
			fmt.Println("[dotkeeper] stopped")
		},
	}
}

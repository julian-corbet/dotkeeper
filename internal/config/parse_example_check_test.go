package config

import (
	"path/filepath"
	"testing"
	"github.com/BurntSushi/toml"
)

func TestExampleTOMLParsesAsRepoConfigV2(t *testing.T) {
	path := filepath.Join("..", "..", "dotkeeper.toml.example")
	var cfg RepoConfigV2
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil { t.Fatalf("decode error: %v", err) }
	if u := md.Undecoded(); len(u) > 0 { t.Fatalf("undecoded keys (schema mismatch): %v", u) }
	if cfg.SchemaVersion != 2 { t.Errorf("schema_version: want 2 got %d", cfg.SchemaVersion) }
	if cfg.Meta.Name == "" { t.Error("repo.name should be set") }
	if cfg.Sync.SyncthingFolderID == "" { t.Error("sync.syncthing_folder_id should be set") }
	if len(cfg.Sync.ShareWith) == 0 { t.Error("sync.share_with should have entries") }
}

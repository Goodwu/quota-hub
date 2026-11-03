package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name string, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if errWrite := os.WriteFile(path, []byte(content), mode); errWrite != nil {
		t.Fatalf("write %s: %v", name, errWrite)
	}
	_ = os.Chmod(path, mode)
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	// HOME 指到空目录：默认配置路径不存在时走全默认值。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("QUOTA_HUB_CONFIG", "")
	t.Setenv("QUOTA_HUB_API_BASE", "")
	t.Setenv("QUOTA_HUB_SOCKET_PATH", "")
	t.Setenv("QUOTA_HUB_MANAGEMENT_KEY_FILE", "")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIBase != defaultAPIBase || cfg.SocketPath == "" || cfg.ManagementKeyFile == "" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.QuotaCacheTTL != defaultQuotaCacheTTL || cfg.RouteCacheTTL != defaultRouteCacheTTL {
		t.Fatalf("ttl defaults: %+v", cfg)
	}
	if cfg.MaxConcurrency != defaultMaxConcurrency {
		t.Fatalf("concurrency: %d", cfg.MaxConcurrency)
	}
}

func TestLoadConfigFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if errWrite := os.WriteFile(path, []byte(`{"api_base":"http://127.0.0.1:9999","quota_cache_ttl_seconds":120,"model_mappings":[{"prefix":"cc","provider":"commandcode-go"}]}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	t.Setenv("QUOTA_HUB_CONFIG", path)
	t.Setenv("QUOTA_HUB_SOCKET_PATH", "/tmp/other.sock")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIBase != "http://127.0.0.1:9999" {
		t.Fatalf("api_base = %s", cfg.APIBase)
	}
	if cfg.SocketPath != "/tmp/other.sock" {
		t.Fatalf("env override socket = %s", cfg.SocketPath)
	}
	if cfg.QuotaCacheTTL != 120e9 {
		t.Fatalf("quota ttl = %v", cfg.QuotaCacheTTL)
	}
	if len(cfg.ModelMappings) != 1 || cfg.ModelMappings[0].Prefix != "cc" {
		t.Fatalf("mappings: %+v", cfg.ModelMappings)
	}
}

func TestLoadConfigExplicitPathMustExist(t *testing.T) {
	t.Setenv("QUOTA_HUB_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := LoadConfig(""); err == nil {
		t.Fatal("explicit config path must fail when missing")
	}
}

func TestLoadManagementKeyFileChecks(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		path := writeTemp(t, "management.key", " my-secret \n", 0o600)
		key, err := LoadManagementKey(path)
		if err != nil || key != "my-secret" {
			t.Fatalf("key = %q err = %v", key, err)
		}
	})
	t.Run("loose mode rejected", func(t *testing.T) {
		path := writeTemp(t, "management.key", "my-secret", 0o644)
		if _, err := LoadManagementKey(path); err == nil {
			t.Fatal("0644 key file must be rejected")
		}
	})
	t.Run("empty rejected", func(t *testing.T) {
		path := writeTemp(t, "management.key", "  ", 0o600)
		if _, err := LoadManagementKey(path); err == nil {
			t.Fatal("empty key must be rejected")
		}
	})
	t.Run("multiline rejected", func(t *testing.T) {
		path := writeTemp(t, "management.key", "a\nb", 0o600)
		if _, err := LoadManagementKey(path); err == nil {
			t.Fatal("multiline key must be rejected")
		}
	})
}

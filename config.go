// quota-hub 配置：~/.quota-hub/config.json + 环境变量覆盖。
// 密钥文件与 socket 属运行时产物，目录 0700；本程序不写入密钥。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaults mirror the design doc (quota 60s / route 30s / total 15s /
// account 10s / concurrency 4 / stale 15min / backoff 60s).
const (
	defaultAPIBase              = "http://127.0.0.1:8317"
	defaultSocketRel            = ".quota-hub/quota-hub.sock"
	defaultKeyFileRel           = ".quota-hub/management.key"
	defaultConfigRel            = ".quota-hub/config.json"
	defaultQuotaCacheTTL        = 60 * time.Second
	defaultRouteCacheTTL        = 30 * time.Second
	defaultStaleAfter           = 15 * time.Minute
	defaultTotalTimeout         = 15 * time.Second
	defaultAccountTimeout       = 10 * time.Second
	defaultMaxConcurrency       = 4
	defaultBackoff              = 60 * time.Second
	defaultManagementKeyMode    = 0o600
	configMaxBytes              = 64 << 10
	managementKeyFileMaxContent = 4 << 10
)

// ModelMapping is one explicit prefix -> provider rule. Prefix matching is by
// whole path segments ("cc/deepseek" matches "cc/deepseek/deepseek-v4-flash",
// never "cc/deepseekx/...").
type ModelMapping struct {
	Prefix   string `json:"prefix"`
	Provider string `json:"provider"`
}

type Config struct {
	APIBase               string         `json:"api_base"`
	SocketPath            string         `json:"socket_path"`
	ManagementKeyFile     string         `json:"management_key_file"`
	ModelMappings         []ModelMapping `json:"model_mappings"`
	PopupProviders        []string       `json:"popup_providers"`
	QuotaCacheTTL         time.Duration  `json:"-"`
	RouteCacheTTL         time.Duration  `json:"-"`
	StaleAfter            time.Duration  `json:"-"`
	TotalTimeout          time.Duration  `json:"-"`
	AccountTimeout        time.Duration  `json:"-"`
	MaxConcurrency        int            `json:"max_concurrency"`
	DefaultBackoff        time.Duration  `json:"-"`
	QuotaCacheTTLSeconds  *int           `json:"quota_cache_ttl_seconds,omitempty"`
	RouteCacheTTLSeconds  *int           `json:"route_cache_ttl_seconds,omitempty"`
	StaleAfterSeconds     *int           `json:"stale_after_seconds,omitempty"`
	TotalTimeoutSeconds   *int           `json:"total_timeout_seconds,omitempty"`
	AccountTimeoutSeconds *int           `json:"account_timeout_seconds,omitempty"`
	DefaultBackoffSeconds *int           `json:"default_backoff_seconds,omitempty"`
}

func defaultConfig() Config {
	return Config{
		APIBase:        defaultAPIBase,
		MaxConcurrency: defaultMaxConcurrency,
	}
}

// LoadConfig reads the JSON config (if present), applies defaults, then env
// overrides. Paths go through expandPath (~ and $HOME).
func LoadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" {
		path = defaultConfigPath()
	}
	if raw, err := os.ReadFile(path); err == nil {
		if len(raw) > configMaxBytes {
			return Config{}, fmt.Errorf("config %s too large", path)
		}
		if errUnmarshal := json.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, errUnmarshal)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	} else if strings.TrimSpace(os.Getenv("QUOTA_HUB_CONFIG")) != "" {
		// Explicit config path must exist.
		return Config{}, fmt.Errorf("config %s not found", path)
	}
	applyEnvOverrides(&cfg)
	if errApply := applyDefaults(&cfg); errApply != nil {
		return Config{}, errApply
	}
	return cfg, nil
}

func defaultConfigPath() string {
	if env := strings.TrimSpace(os.Getenv("QUOTA_HUB_CONFIG")); env != "" {
		return expandPath(env)
	}
	return filepath.Join(homeDir(), defaultConfigRel)
}

func applyEnvOverrides(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("QUOTA_HUB_API_BASE")); v != "" {
		cfg.APIBase = v
	}
	if v := strings.TrimSpace(os.Getenv("QUOTA_HUB_SOCKET_PATH")); v != "" {
		cfg.SocketPath = v
	}
	if v := strings.TrimSpace(os.Getenv("QUOTA_HUB_MANAGEMENT_KEY_FILE")); v != "" {
		cfg.ManagementKeyFile = v
	}
}

func applyDefaults(cfg *Config) error {
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = defaultAPIBase
	}
	cfg.APIBase = strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	// 回环与 scheme 校验在 NewMgmtClient 中执行（唯一消费方）。
	if strings.TrimSpace(cfg.SocketPath) == "" {
		cfg.SocketPath = filepath.Join(homeDir(), defaultSocketRel)
	}
	cfg.SocketPath = expandPath(cfg.SocketPath)
	if strings.TrimSpace(cfg.ManagementKeyFile) == "" {
		cfg.ManagementKeyFile = filepath.Join(homeDir(), defaultKeyFileRel)
	}
	cfg.ManagementKeyFile = expandPath(cfg.ManagementKeyFile)
	for i := range cfg.ModelMappings {
		cfg.ModelMappings[i].Prefix = strings.Trim(strings.TrimSpace(cfg.ModelMappings[i].Prefix), "/")
		cfg.ModelMappings[i].Provider = strings.TrimSpace(cfg.ModelMappings[i].Provider)
	}
	cfg.QuotaCacheTTL = secondsOrDefault(cfg.QuotaCacheTTLSeconds, defaultQuotaCacheTTL)
	cfg.RouteCacheTTL = secondsOrDefault(cfg.RouteCacheTTLSeconds, defaultRouteCacheTTL)
	cfg.StaleAfter = secondsOrDefault(cfg.StaleAfterSeconds, defaultStaleAfter)
	cfg.TotalTimeout = secondsOrDefault(cfg.TotalTimeoutSeconds, defaultTotalTimeout)
	cfg.AccountTimeout = secondsOrDefault(cfg.AccountTimeoutSeconds, defaultAccountTimeout)
	cfg.DefaultBackoff = secondsOrDefault(cfg.DefaultBackoffSeconds, defaultBackoff)
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = defaultMaxConcurrency
	}
	if cfg.MaxConcurrency > 32 {
		return fmt.Errorf("max_concurrency too large: %d", cfg.MaxConcurrency)
	}
	return nil
}

func secondsOrDefault(value *int, fallback time.Duration) time.Duration {
	if value == nil || *value <= 0 {
		return fallback
	}
	return time.Duration(*value) * time.Second
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return os.Getenv("HOME")
}

func expandPath(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

// LoadManagementKey reads the management key from its private file. It must
// be mode 0600 and contain a single non-empty line; the value stays in
// memory only.
func LoadManagementKey(path string) (string, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return "", fmt.Errorf("read management key file: %w", errRead)
	}
	if len(raw) > managementKeyFileMaxContent {
		return "", fmt.Errorf("management key file too large")
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return "", fmt.Errorf("stat management key file: %w", errStat)
	}
	if perm := info.Mode().Perm(); perm != defaultManagementKeyMode {
		return "", fmt.Errorf("management key file mode is %o, want 0600", perm)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", fmt.Errorf("management key file is empty")
	}
	if strings.ContainsAny(key, "\n\r\x00") {
		return "", fmt.Errorf("management key must be a single line")
	}
	return key, nil
}

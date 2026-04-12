package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	BindModeAuto      = "auto"
	BindModeForce     = "force"
	BindModeTokenOnly = "token_only"
)

type Config struct {
	HTTPAddr           string `yaml:"http_addr"`
	MachineID          string `yaml:"machine_id"`
	MachineToken       string `yaml:"machine_token"`
	BindMode           string `yaml:"bind_mode"`
	MaxConcurrentTurns int    `yaml:"max_concurrent_turns"`
	SQLitePath         string `yaml:"sqlite_path"`
	ServerBaseURL      string `yaml:"server_base_url"`
	ServerWSURL        string `yaml:"server_ws_url"`
	CodexBin           string `yaml:"codex_bin"`
	CodexModel         string `yaml:"codex_model"`
	GeminiBin          string `yaml:"gemini_bin"`
	GeminiModel        string `yaml:"gemini_model"`
	ClaudeBin          string `yaml:"claude_bin"`
	ClaudeModel        string `yaml:"claude_model"`
}
type fileConfig struct {
	Daemon Config `yaml:"daemon"`
}

// HomeDir returns the codewithphone home directory (~/.codewithphone).
func HomeDir() string {
	if dir := os.Getenv("CODEWITHPHONE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".codewithphone")
}

// PIDPath returns the path to the PID file.
func PIDPath() string {
	return filepath.Join(HomeDir(), "codewithphone.pid")
}

// LogPath returns the path to the log file used in daemon mode.
func LogPath() string {
	return filepath.Join(HomeDir(), "codewithphone.log")
}

func Default() Config {
	defaultServerBaseURL := "https://codewithphone.com/api"
	defaultServerWSURL := "wss://codewithphone.com/api/ws/daemon"

	homeDir := HomeDir()
	defaultSQLitePath := filepath.Join(homeDir, "data", "agent.db")

	return Config{
		HTTPAddr:           envOrDefault("CODEWITHPHONE_ADDR", envOrDefault("DAEMON_HTTP_ADDR", "127.0.0.1:0")),
		MachineID:          envOrDefault("DAEMON_MACHINE_ID", ""),
		MachineToken:       envOrDefault("DAEMON_MACHINE_TOKEN", ""),
		BindMode:           envOrDefault("DAEMON_BIND_MODE", BindModeAuto),
		MaxConcurrentTurns: normalizeMaxConcurrentTurns(envIntOrDefault("DAEMON_MAX_CONCURRENT_TURNS", 10)),
		SQLitePath:         envOrDefault("DAEMON_SQLITE_PATH", defaultSQLitePath),
		ServerBaseURL:      envOrDefault("DAEMON_SERVER_BASE_URL", defaultServerBaseURL),
		ServerWSURL:        envOrDefault("DAEMON_SERVER_WS_URL", defaultServerWSURL),
		CodexBin:           envOrDefault("DAEMON_CODEX_BIN", "codex"),
		CodexModel:         envOrDefault("DAEMON_CODEX_MODEL", ""),
		GeminiBin:          envOrDefault("DAEMON_GEMINI_BIN", "gemini"),
		GeminiModel:        envOrDefault("DAEMON_GEMINI_MODEL", "gemini-3-flash-preview"),
		ClaudeBin:          envOrDefault("DAEMON_CLAUDE_BIN", "claude"),
		ClaudeModel:        envOrDefault("DAEMON_CLAUDE_MODEL", "sonnet"),
	}
}
func Load() (Config, error) {
	cfg := Default()

	path, explicit := DiscoverConfigPath()
	if path != "" {
		payload, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}

		var parsed fileConfig
		if err := yaml.Unmarshal(payload, &parsed); err != nil {
			return Config{}, err
		}
		if parsed.Daemon.HTTPAddr != "" {
			cfg.HTTPAddr = parsed.Daemon.HTTPAddr
		}
		if parsed.Daemon.MachineID != "" {
			cfg.MachineID = parsed.Daemon.MachineID
		}
		if parsed.Daemon.MachineToken != "" {
			cfg.MachineToken = parsed.Daemon.MachineToken
		}
		if parsed.Daemon.BindMode != "" {
			cfg.BindMode = parsed.Daemon.BindMode
		}
		if parsed.Daemon.MaxConcurrentTurns > 0 {
			cfg.MaxConcurrentTurns = normalizeMaxConcurrentTurns(parsed.Daemon.MaxConcurrentTurns)
		}
		if parsed.Daemon.SQLitePath != "" {
			cfg.SQLitePath = parsed.Daemon.SQLitePath
		}
		if parsed.Daemon.ServerBaseURL != "" {
			cfg.ServerBaseURL = parsed.Daemon.ServerBaseURL
		}
		if parsed.Daemon.ServerWSURL != "" {
			cfg.ServerWSURL = parsed.Daemon.ServerWSURL
		}
		if parsed.Daemon.CodexBin != "" {
			cfg.CodexBin = parsed.Daemon.CodexBin
		}
		if parsed.Daemon.CodexModel != "" {
			cfg.CodexModel = parsed.Daemon.CodexModel
		}
		if parsed.Daemon.GeminiBin != "" {
			cfg.GeminiBin = parsed.Daemon.GeminiBin
		}
		if parsed.Daemon.GeminiModel != "" {
			cfg.GeminiModel = parsed.Daemon.GeminiModel
		}
		if parsed.Daemon.ClaudeBin != "" {
			cfg.ClaudeBin = parsed.Daemon.ClaudeBin
		}
		if parsed.Daemon.ClaudeModel != "" {
			cfg.ClaudeModel = parsed.Daemon.ClaudeModel
		}
	} else if explicit {
		return Config{}, errors.New("config path was provided but no config file was found")
	}

	override(&cfg.HTTPAddr, "DAEMON_HTTP_ADDR")
	override(&cfg.HTTPAddr, "CODEWITHPHONE_ADDR")
	override(&cfg.MachineID, "DAEMON_MACHINE_ID")
	override(&cfg.MachineToken, "DAEMON_MACHINE_TOKEN")
	override(&cfg.BindMode, "DAEMON_BIND_MODE")
	if value, ok := envInt("DAEMON_MAX_CONCURRENT_TURNS"); ok {
		cfg.MaxConcurrentTurns = normalizeMaxConcurrentTurns(value)
	}
	override(&cfg.SQLitePath, "DAEMON_SQLITE_PATH")
	override(&cfg.ServerBaseURL, "DAEMON_SERVER_BASE_URL")
	override(&cfg.ServerWSURL, "DAEMON_SERVER_WS_URL")
	override(&cfg.CodexBin, "DAEMON_CODEX_BIN")
	override(&cfg.CodexModel, "DAEMON_CODEX_MODEL")
	override(&cfg.GeminiBin, "DAEMON_GEMINI_BIN")
	override(&cfg.GeminiModel, "DAEMON_GEMINI_MODEL")
	override(&cfg.ClaudeBin, "DAEMON_CLAUDE_BIN")
	override(&cfg.ClaudeModel, "DAEMON_CLAUDE_MODEL")

	bindMode, err := ParseBindMode(cfg.BindMode)
	if err != nil {
		return Config{}, err
	}
	cfg.BindMode = bindMode

	return cfg, nil
}

func ParseBindMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return BindModeAuto, nil
	}
	switch mode {
	case BindModeAuto, BindModeForce, BindModeTokenOnly:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid daemon.bind_mode %q (expected %s|%s|%s)", raw, BindModeAuto, BindModeForce, BindModeTokenOnly)
	}
}

func (cfg Config) Save() error {
	homeDir := HomeDir()
	_ = os.MkdirAll(homeDir, 0o700)
	_ = os.Chmod(homeDir, 0o700)

	path, _ := DiscoverConfigPath()
	if path == "" {
		path = filepath.Join(homeDir, "config.yaml")
	}

	// Load current file to preserve other sections if any
	current := make(map[string]any)
	if payload, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(payload, &current)
	}

	current["daemon"] = cfg

	payload, err := yaml.Marshal(current)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func DiscoverConfigPath() (string, bool) {
	if explicit := os.Getenv("CODEWITHPHONE_CONFIG"); explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, true
		}
		return "", true
	}
	// Legacy env var support.
	if explicit := os.Getenv("POCKETCODE_CONFIG"); explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, true
		}
		return "", true
	}

	candidates := []string{
		filepath.Join(HomeDir(), "config.yaml"),
		"config.yaml",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, false
		}
	}
	return "", false
}

func override(target *string, key string) {
	if value := os.Getenv(key); value != "" {
		*target = value
	}
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envIntOrDefault(key string, fallback int) int {
	value, ok := envInt(key)
	if !ok {
		return fallback
	}
	return value
}

func envInt(key string) (int, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func normalizeMaxConcurrentTurns(value int) int {
	const minConcurrentTurns = 1
	const maxConcurrentTurns = 32

	if value < minConcurrentTurns {
		return minConcurrentTurns
	}
	if value > maxConcurrentTurns {
		return maxConcurrentTurns
	}
	return value
}

// Package config loads and persists the server configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickwork-openai/internal/outbound"
)

// Duration is a time.Duration encoded as a Go duration string ("5m", "1h").
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\" or seconds")
	}
	*d = Duration(seconds * float64(time.Second))
	return nil
}

// OAuth holds defaults used by the `login` command.
type OAuth struct {
	Provider     string   `json:"provider"`
	BuildStage   string   `json:"build_stage"`
	Region       string   `json:"region"`
	CallbackPort int      `json:"callback_port"`
	Timeout      Duration `json:"timeout"`
}

type WebUI struct {
	Enabled    bool     `json:"enabled"`
	SessionTTL Duration `json:"session_ttl"`
}

type Usage struct {
	Enabled         bool     `json:"enabled"`
	RefreshInterval Duration `json:"refresh_interval"`
	MaxAttempts     int      `json:"max_attempts"`
}

// Config is the server configuration stored in config.json.
type Config struct {
	Addr             string   `json:"addr"`
	Database         string   `json:"database"`
	ProxyID          string   `json:"proxy_id"`
	APIKeys          []string `json:"api_keys"`
	Models           []string `json:"models"`
	DefaultModel     string   `json:"default_model"`
	RequestTimeout   Duration `json:"request_timeout"`
	RefreshBefore    Duration `json:"refresh_before"`
	MaxAttempts      int      `json:"max_attempts"`
	AppVersion       string   `json:"app_version"`
	Surface          string   `json:"surface"`
	OutpostID        string   `json:"outpost_id"`
	WorkingSessionID string   `json:"working_session_id"`
	Timezone         string   `json:"timezone"`
	Locale           string   `json:"locale"`
	ThinkingEffort   string   `json:"thinking_effort"`
	ChatAgentID      string   `json:"chat_agent_id"`
	KeepSessions     bool     `json:"keep_sessions"`
	SessionReuse     bool     `json:"session_reuse"`
	NeutralPrompt    bool     `json:"neutral_prompt"`
	NeutralToolLimit int      `json:"neutral_tool_limit"`
	SessionTTL       Duration `json:"session_ttl"`
	OAuth            OAuth    `json:"oauth"`
	WebUI            WebUI    `json:"webui"`
	Usage            Usage    `json:"usage"`

	// Path is the absolute config file path; not serialized.
	Path    string           `json:"-"`
	Proxies *outbound.Router `json:"-"`

	originalDatabase string
	resolvedDatabase string
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Addr:             "127.0.0.1:8080",
		Database:         "data/quickwork.db",
		APIKeys:          []string{},
		Models:           []string{"smart", "fast", "balanced", "auto"},
		DefaultModel:     "smart",
		RequestTimeout:   Duration(5 * time.Minute),
		RefreshBefore:    Duration(10 * time.Minute),
		MaxAttempts:      3,
		AppVersion:       "1.2.4660",
		Surface:          "QUICK_WORK_DESKTOP_WINDOWS",
		Timezone:         "UTC",
		Locale:           "en-US",
		ThinkingEffort:   "",
		KeepSessions:     false,
		SessionReuse:     true,
		NeutralPrompt:    envEnabled("QUICKPROXY_NEUTRAL_PROMPT"),
		NeutralToolLimit: envInt("QUICKPROXY_NEUTRAL_TOOL_LIMIT", 16),
		SessionTTL:       Duration(30 * time.Minute),
		OAuth: OAuth{
			Provider:     "social",
			BuildStage:   "prod",
			CallbackPort: 18080,
			Timeout:      Duration(time.Hour),
		},
		WebUI: WebUI{
			Enabled:    true,
			SessionTTL: Duration(12 * time.Hour),
		},
		Usage: Usage{
			Enabled:         true,
			RefreshInterval: Duration(5 * time.Minute),
			MaxAttempts:     3,
		},
	}
}

// Load reads configPath, creating it with defaults when it does not exist.
// Relative paths inside the config (database) are resolved against the
// config file's directory so the server behaves the same from any cwd.
func Load(configPath string) (*Config, error) {
	if strings.TrimSpace(configPath) == "" {
		configPath = "config.json"
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	body, err := os.ReadFile(abs)
	switch {
	case err == nil:
		if err := json.Unmarshal(body, &cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", abs, err)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := write(abs, &cfg); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	cfg.Path = abs
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	cfg.originalDatabase = cfg.Database
	if !filepath.IsAbs(cfg.Database) {
		cfg.Database = filepath.Join(filepath.Dir(abs), cfg.Database)
	}
	cfg.resolvedDatabase = cfg.Database
	return &cfg, nil
}

func (c *Config) FileValues() Config {
	value := *c
	if c.originalDatabase != "" && c.Database == c.resolvedDatabase {
		value.Database = c.originalDatabase
	}
	return value
}

func write(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".quickproxy-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (c *Config) Save() error {
	if c.Path == "" {
		return errors.New("config path is empty")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	value := c.FileValues()
	return write(c.Path, &value)
}

func applyDefaults(c *Config) {
	d := Default()
	if strings.TrimSpace(c.Addr) == "" {
		c.Addr = d.Addr
	}
	if strings.TrimSpace(c.Database) == "" {
		c.Database = d.Database
	}
	if len(c.Models) == 0 {
		c.Models = d.Models
	}
	if strings.TrimSpace(c.DefaultModel) == "" {
		c.DefaultModel = d.DefaultModel
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = d.RequestTimeout
	}
	if c.RefreshBefore < 0 {
		c.RefreshBefore = d.RefreshBefore
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = d.SessionTTL
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if strings.TrimSpace(c.AppVersion) == "" {
		c.AppVersion = d.AppVersion
	}
	if strings.TrimSpace(c.Surface) == "" {
		c.Surface = d.Surface
	}
	if strings.TrimSpace(c.Timezone) == "" {
		c.Timezone = d.Timezone
	}
	if strings.TrimSpace(c.Locale) == "" {
		c.Locale = d.Locale
	}
	if strings.TrimSpace(c.OAuth.Provider) == "" {
		c.OAuth.Provider = d.OAuth.Provider
	}
	if strings.TrimSpace(c.OAuth.BuildStage) == "" {
		c.OAuth.BuildStage = d.OAuth.BuildStage
	}
	if c.OAuth.CallbackPort <= 0 {
		c.OAuth.CallbackPort = d.OAuth.CallbackPort
	}
	if c.OAuth.Timeout <= 0 {
		c.OAuth.Timeout = d.OAuth.Timeout
	}
	if c.WebUI.SessionTTL <= 0 {
		c.WebUI.SessionTTL = d.WebUI.SessionTTL
	}
	if c.Usage.RefreshInterval <= 0 {
		c.Usage.RefreshInterval = d.Usage.RefreshInterval
	}
	if c.Usage.MaxAttempts <= 0 {
		c.Usage.MaxAttempts = d.Usage.MaxAttempts
	}
}

func (c *Config) Validate() error {
	if c.ProxyID != "" && c.ProxyID != "direct" {
		if len(c.ProxyID) != 64 || strings.Trim(c.ProxyID, "0123456789abcdef") != "" {
			return fmt.Errorf("proxy_id must be empty, direct or an ID from the proxy list")
		}
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request_timeout must be positive")
	}
	if c.OAuth.CallbackPort < 0 || c.OAuth.CallbackPort > 65535 {
		return fmt.Errorf("oauth.callback_port must be 0-65535")
	}
	if c.WebUI.SessionTTL < Duration(time.Minute) || c.WebUI.SessionTTL > Duration(7*24*time.Hour) {
		return fmt.Errorf("webui.session_ttl must be between 1m and 168h")
	}
	if c.Usage.RefreshInterval < Duration(time.Minute) || c.Usage.RefreshInterval > Duration(24*time.Hour) {
		return fmt.Errorf("usage.refresh_interval must be between 1m and 24h")
	}
	if c.Usage.MaxAttempts < 1 || c.Usage.MaxAttempts > 5 {
		return fmt.Errorf("usage.max_attempts must be between 1 and 5")
	}
	switch strings.ToLower(c.OAuth.Provider) {
	case "social", "enterprise", "gateway", "social_gateway":
	default:
		return fmt.Errorf("oauth.provider must be social, enterprise, gateway or social_gateway")
	}
	switch strings.ToLower(c.OAuth.BuildStage) {
	case "prod", "beta", "integ", "dev":
	default:
		return fmt.Errorf("oauth.build_stage must be prod, beta, integ or dev")
	}
	return nil
}

// envEnabled reports whether an environment switch is set to a truthy value.
func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envInt reads a positive integer environment override.
func envInt(name string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

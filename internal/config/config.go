// Package config loads and validates cloak.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Action is a per-category policy action.
type Action string

const (
	ActionRedact Action = "redact"
	ActionBlock  Action = "block"
	ActionAllow  Action = "allow"
	ActionAudit  Action = "audit"
)

// Config is the top-level Cloak configuration.
type Config struct {
	Listen     string            `yaml:"listen"`
	Upstream   UpstreamConfig    `yaml:"upstream"`
	Lemonade   LemonadeConfig    `yaml:"lemonade"`
	Detection  DetectionConfig   `yaml:"detection"`
	Policy     map[string]Action `yaml:"policy"`
	Watchlist  []string          `yaml:"watchlist"`
	Allowlist  []string          `yaml:"allowlist"`
	Persist    bool              `yaml:"persist_entities"`
	DataDir    string            `yaml:"data_dir"`
	LogLevel   string            `yaml:"log_level"`
	Audit      bool              `yaml:"audit"`
}

// UpstreamConfig describes the cloud provider.
type UpstreamConfig struct {
	BaseURL             string `yaml:"base_url"`
	APIKeyEnv           string `yaml:"api_key_env"`
	APIKey              string `yaml:"api_key"` // optional direct key (discouraged)
	AnthropicBaseURL    string `yaml:"anthropic_base_url"`
	AnthropicAPIKeyEnv  string `yaml:"anthropic_api_key_env"`
	AnthropicAPIKey     string `yaml:"anthropic_api_key"`
}

// LemonadeConfig describes the local detector + omni ASR + private mode.
type LemonadeConfig struct {
	BaseURL      string `yaml:"base_url"`
	Model        string `yaml:"model"`
	WhisperModel string `yaml:"whisper_model"`
	ChatModel    string `yaml:"chat_model"`   // local model used to answer Private Mode prompts (default: Model)
	TimeoutMS    int    `yaml:"timeout_ms"`
	Enabled      bool   `yaml:"enabled"`
	OmniASR      bool   `yaml:"omni_asr"`     // POST /v1/audio/transcriptions via Lemonade Whisper
	PrivateMode  bool   `yaml:"private_mode"` // answer blocked/crown-jewel prompts locally instead of erroring
}

// PrivateChatModel returns the local model to answer Private Mode prompts.
func (l LemonadeConfig) PrivateChatModel() string {
	if l.ChatModel != "" {
		return l.ChatModel
	}
	return l.Model
}

// DetectionConfig controls the detection pipeline.
type DetectionConfig struct {
	Tier2FailOpenSoft  bool `yaml:"tier2_fail_open_soft"`
	FailClosedSecrets  bool `yaml:"fail_closed_secrets"`
	CacheMessageHashes bool `yaml:"cache_message_hashes"`
}

// Default returns a sensible default configuration.
func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Listen: "127.0.0.1:7777",
		Upstream: UpstreamConfig{
			BaseURL:            "https://api.openai.com/v1",
			APIKeyEnv:          "OPENAI_API_KEY",
			AnthropicBaseURL:   "https://api.anthropic.com",
			AnthropicAPIKeyEnv: "ANTHROPIC_API_KEY",
		},
		Lemonade: LemonadeConfig{
			BaseURL:      "http://127.0.0.1:13305/api/v1",
			Model:        "Qwen3-4B-Instruct-2507-GGUF",
			WhisperModel: "Whisper-Tiny",
			TimeoutMS:    2500,
			Enabled:      true,
			OmniASR:      true,
			PrivateMode:  true,
		},
		Detection: DetectionConfig{
			Tier2FailOpenSoft:  true,
			FailClosedSecrets:  true,
			CacheMessageHashes: true,
		},
		Policy: defaultPolicy(),
		Watchlist: []string{},
		Allowlist: []string{},
		Persist:   false,
		DataDir:   filepath.Join(home, ".local", "share", "cloak"),
		LogLevel:  "info",
		Audit:     true,
	}
}

func defaultPolicy() map[string]Action {
	return map[string]Action{
		"EMAIL":             ActionRedact,
		"PHONE":             ActionRedact,
		"SSN":               ActionRedact,
		"CREDIT_CARD":       ActionBlock,
		"AWS_KEY":           ActionBlock,
		"GCP_KEY":           ActionBlock,
		"GITHUB_TOKEN":      ActionBlock,
		"SLACK_TOKEN":       ActionBlock,
		"OPENAI_KEY":        ActionBlock,
		"STRIPE_KEY":        ActionBlock,
		"JWT":               ActionRedact,
		"PRIVATE_KEY":       ActionBlock,
		"IP":                ActionRedact,
		"MAC":               ActionAllow,
		"PERSON":            ActionRedact,
		"ORG":               ActionRedact,
		"ADDRESS":           ActionRedact,
		"PROJECT_CODENAME":  ActionRedact,
		"INTERNAL_HOSTNAME": ActionRedact,
		"MEDICAL":           ActionRedact,
		"FINANCIAL":         ActionRedact,
		"OTHER_ID":          ActionRedact,
		"HIGH_ENTROPY":      ActionRedact,
		"URL_CREDENTIAL":    ActionRedact,
	}
}

// Load reads config from path, falling back to defaults for missing fields.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		path = Find()
	}
	if path == "" {
		cfg.resolveKeys()
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:7777"
	}
	if cfg.DataDir != "" && strings.HasPrefix(cfg.DataDir, "~/") {
		home, _ := os.UserHomeDir()
		cfg.DataDir = filepath.Join(home, cfg.DataDir[2:])
	}
	if cfg.Policy == nil {
		cfg.Policy = defaultPolicy()
	} else {
		// merge defaults for missing categories
		for k, v := range defaultPolicy() {
			if _, ok := cfg.Policy[k]; !ok {
				cfg.Policy[k] = v
			}
		}
	}
	cfg.resolveKeys()
	return cfg, nil
}

func (c *Config) resolveKeys() {
	if c.Upstream.APIKey == "" && c.Upstream.APIKeyEnv != "" {
		c.Upstream.APIKey = os.Getenv(c.Upstream.APIKeyEnv)
	}
	if c.Upstream.AnthropicAPIKey == "" && c.Upstream.AnthropicAPIKeyEnv != "" {
		c.Upstream.AnthropicAPIKey = os.Getenv(c.Upstream.AnthropicAPIKeyEnv)
	}
}

// Find locates a config file in standard locations.
func Find() string {
	candidates := []string{"cloak.yaml", "cloak.yml"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "cloak", "cloak.yaml"),
			filepath.Join(home, ".cloak.yaml"),
		)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "cloak", "cloak.yaml"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// UpstreamAPIKey returns the resolved OpenAI-compatible API key.
func (c *Config) UpstreamAPIKey() string { return c.Upstream.APIKey }

// AnthropicAPIKey returns the resolved Anthropic API key.
func (c *Config) AnthropicKey() string { return c.Upstream.AnthropicAPIKey }

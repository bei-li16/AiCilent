package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	ext := filepath.Ext(path)
	cfg := &Config{}

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format: %s", ext)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	cfg.applyDefaults()
	cfg.sortProviders()
	return cfg, nil
}

func (cfg *Config) validate() error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	names := make(map[string]bool)
	for _, p := range cfg.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider name is required")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate provider name: %s", p.Name)
		}
		names[p.Name] = true
		if p.APIKey == "" {
			return fmt.Errorf("provider %s: api_key is required", p.Name)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %s: base_url is required", p.Name)
		}
		if p.Format != "openai" && p.Format != "anthropic" {
			return fmt.Errorf("provider %s: format must be 'openai' or 'anthropic'", p.Name)
		}
		if p.Retry.MaxRetries < 0 {
			return fmt.Errorf("provider %s: max_retries cannot be negative", p.Name)
		}
		if p.Retry.RetryInterval < 0 {
			return fmt.Errorf("provider %s: retry_interval cannot be negative", p.Name)
		}
		if p.Retry.BackoffFactor < 0 {
			return fmt.Errorf("provider %s: backoff_factor cannot be negative", p.Name)
		}
		if p.Timeout < 0 {
			return fmt.Errorf("provider %s: timeout cannot be negative", p.Name)
		}
	}
	return nil
}

func (cfg *Config) applyDefaults() {
	if cfg.Global.ListenAddr == "" {
		cfg.Global.ListenAddr = ":8080"
	}
	if cfg.Global.CBThreshold <= 0 {
		cfg.Global.CBThreshold = 2
	}
	if cfg.Global.CBCooldown <= 0 {
		cfg.Global.CBCooldown = 30
	}
	if cfg.Global.CBSkipRequests <= 0 {
		cfg.Global.CBSkipRequests = 10
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].Retry.MaxRetries <= 0 {
			cfg.Providers[i].Retry.MaxRetries = 3
		}
		if cfg.Providers[i].Retry.RetryInterval <= 0 {
			cfg.Providers[i].Retry.RetryInterval = 2
		}
		if cfg.Providers[i].Retry.BackoffFactor <= 0 {
			cfg.Providers[i].Retry.BackoffFactor = 2
		}
		if cfg.Providers[i].Timeout <= 0 {
			cfg.Providers[i].Timeout = 60
		}
		// Auto-detect auth type from format if not explicitly set
		if cfg.Providers[i].AuthType == "" {
			if cfg.Providers[i].Format == "anthropic" {
				cfg.Providers[i].AuthType = "x-api-key"
			} else {
				cfg.Providers[i].AuthType = "bearer"
			}
		}
		if cfg.Providers[i].RateLimit.RPM <= 0 {
			cfg.Providers[i].RateLimit.RPM = 60 // default 60 RPM
		}
		if cfg.Providers[i].RateLimit.Burst <= 0 {
			cfg.Providers[i].RateLimit.Burst = 10
		}
	}
}

func (cfg *Config) sortProviders() {
	sort.SliceStable(cfg.Providers, func(i, j int) bool {
		return cfg.Providers[i].Priority < cfg.Providers[j].Priority
	})
}
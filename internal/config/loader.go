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
	cfg.SortProviders()
	return cfg, nil
}

func (cfg *Config) validate() error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	if cfg.Global.LogRequestBody != "" && cfg.Global.LogRequestBody != "off" && cfg.Global.LogRequestBody != "snippet" && cfg.Global.LogRequestBody != "full" {
		return fmt.Errorf("global log_request_body must be 'off', 'snippet', or 'full'")
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
		if p.AuthType != "" && p.AuthType != "bearer" && p.AuthType != "x-api-key" {
			return fmt.Errorf("provider %s: auth_type must be 'bearer' or 'x-api-key'", p.Name)
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
		if p.MaxConcurrent < 0 {
			return fmt.Errorf("provider %s: max_concurrent cannot be negative", p.Name)
		}
	}
	ruleNames := make(map[string]bool, len(cfg.ModelRules))
	for _, rule := range cfg.ModelRules {
		if rule.Model == "" {
			return fmt.Errorf("model rule model is required")
		}
		if ruleNames[rule.Model] {
			return fmt.Errorf("duplicate model rule: %s", rule.Model)
		}
		ruleNames[rule.Model] = true
		for key, value := range rule.Defaults {
			if key == "timeout" {
				if n, ok := numberAsInt(value); !ok || n <= 0 {
					return fmt.Errorf("model rule %s: timeout must be a positive integer", rule.Model)
				}
			}
		}
	}
	return nil
}

func numberAsInt(value interface{}) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), uint64(n) <= uint64(^uint(0)>>1)
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), uint64(n) <= uint64(^uint(0)>>1)
	case uint64:
		return int(n), n <= uint64(^uint(0)>>1)
	case float32:
		return int(n), float32(int(n)) == n
	case float64:
		return int(n), float64(int(n)) == n
	default:
		return 0, false
	}
}

func (cfg *Config) applyDefaults() {
	if cfg.Global.ListenAddr == "" {
		cfg.Global.ListenAddr = ":8080"
	}
	if cfg.Global.LogRequestBody == "" {
		cfg.Global.LogRequestBody = "snippet"
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
	if cfg.Global.MaxStreamMinutes <= 0 {
		cfg.Global.MaxStreamMinutes = 3
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

func (cfg *Config) SortProviders() {
	sort.SliceStable(cfg.Providers, func(i, j int) bool {
		return cfg.Providers[i].Priority < cfg.Providers[j].Priority
	})
}

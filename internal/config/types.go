package config

type Config struct {
	Global      GlobalConfig   `yaml:"global" json:"global"`
	Providers   []Provider     `yaml:"providers" json:"providers"`
	ModelRoutes []ModelRoute   `yaml:"model_routes" json:"model_routes"`
}

type GlobalConfig struct {
	DefaultFormat       string `yaml:"default_format" json:"default_format"`
	HealthCheckInterval int    `yaml:"health_check_interval" json:"health_check_interval"`
	ListenAddr          string `yaml:"listen_addr" json:"listen_addr"`
	LogFile             string `yaml:"log_file" json:"log_file"`
	CBThreshold         int    `yaml:"cb_threshold" json:"cb_threshold"`
	CBCooldown          int    `yaml:"cb_cooldown" json:"cb_cooldown"`
	CBSkipRequests      int    `yaml:"cb_skip_requests" json:"cb_skip_requests"`
}

type RetryConfig struct {
	MaxRetries     int     `yaml:"max_retries" json:"max_retries"`
	RetryInterval  int     `yaml:"retry_interval" json:"retry_interval"`
	BackoffFactor  float64 `yaml:"backoff_factor" json:"backoff_factor"`
}

type Provider struct {
	Name     string      `yaml:"name" json:"name"`
	Vendor   string      `yaml:"vendor" json:"vendor"`
	ModelID  string      `yaml:"model_id" json:"model_id"`
	APIKey   string      `yaml:"api_key" json:"api_key"`
	BaseURL  string      `yaml:"base_url" json:"base_url"`
	Priority int         `yaml:"priority" json:"priority"`
	Retry    RetryConfig `yaml:"retry" json:"retry"`
	Timeout  int         `yaml:"timeout" json:"timeout"`
	Format   string      `yaml:"format" json:"format"`
}

type ModelRoute struct {
	Alias  string `yaml:"alias" json:"alias"`
	Target string `yaml:"target" json:"target"`
}
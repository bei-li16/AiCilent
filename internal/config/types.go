package config

type Config struct {
	Global      GlobalConfig   `yaml:"global" json:"global"`
	Providers   []Provider     `yaml:"providers" json:"providers"`
	ModelRoutes []ModelRoute   `yaml:"model_routes" json:"model_routes"`
}

type GlobalConfig struct {
	ListenAddr     string `yaml:"listen_addr" json:"listen_addr"`
	LogFile        string `yaml:"log_file" json:"log_file"`
	// LogRequestBody 控制请求内容日志级别：
	//   "off"     不打印请求内容
	//   "snippet" 仅打印 messages[0] 的前 80 字符（默认）
	//   "full"   打印完整请求结构（model / messages / role / stream / tools 等全部字段）
	LogRequestBody string `yaml:"log_request_body" json:"log_request_body"`
	CBThreshold    int    `yaml:"cb_threshold" json:"cb_threshold"`
	CBCooldown     int    `yaml:"cb_cooldown" json:"cb_cooldown"`
	CBSkipRequests int    `yaml:"cb_skip_requests" json:"cb_skip_requests"`
	// ControlAllowRemote 控制台启停开关 (/api/control) 是否允许远程访问。
	// 默认 false：仅允许 loopback (127.0.0.1/::1) 调用，防止远程他人停掉代理。
	// 设为 true 才允许任意来源（如需远程管理时开启）。
	ControlAllowRemote bool `yaml:"control_allow_remote" json:"control_allow_remote"`
}

type RetryConfig struct {
	MaxRetries     int     `yaml:"max_retries" json:"max_retries"`
	RetryInterval  int     `yaml:"retry_interval" json:"retry_interval"`
	BackoffFactor  float64 `yaml:"backoff_factor" json:"backoff_factor"`
}

type RateLimitConfig struct {
	Enabled bool    `yaml:"enabled" json:"enabled"`
	RPM     float64 `yaml:"rpm" json:"rpm"`         // requests per minute
	Burst   int     `yaml:"burst" json:"burst"`     // max burst
}

type Provider struct {
	Name      string          `yaml:"name" json:"name"`
	ModelID   string          `yaml:"model_id" json:"model_id"`
	APIKey    string          `yaml:"api_key" json:"api_key"`
	BaseURL   string          `yaml:"base_url" json:"base_url"`
	Priority  int             `yaml:"priority" json:"priority"`
	Retry     RetryConfig     `yaml:"retry" json:"retry"`
	Timeout   int             `yaml:"timeout" json:"timeout"`
	Format    string          `yaml:"format" json:"format"`
	AuthType  string          `yaml:"auth_type" json:"auth_type"` // "bearer" or "x-api-key"; auto-detect from format if empty
	RateLimit RateLimitConfig `yaml:"rate_limit" json:"rate_limit"`
}

type ModelRoute struct {
	Alias  string `yaml:"alias" json:"alias"`
	Target string `yaml:"target" json:"target"`
}
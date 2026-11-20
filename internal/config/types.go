package config

type Config struct {
	Global      GlobalConfig `yaml:"global" json:"global"`
	Providers   []Provider   `yaml:"providers" json:"providers"`
	ModelRoutes []ModelRoute `yaml:"model_routes" json:"model_routes"`
	ModelRules  []ModelRule  `yaml:"model_rules" json:"model_rules"`
}

type GlobalConfig struct {
	ListenAddr    string `yaml:"listen_addr" json:"listen_addr"`
	DefaultFormat string `yaml:"default_format" json:"default_format"`
	LogFile       string `yaml:"log_file" json:"log_file"`
	// ReasoningEffortOnNone replaces explicit none/disabled across both formats.
	// An empty value omits that control, leaving the upstream default in charge.
	ReasoningEffortOnNone string `yaml:"reasoning_effort_on_none,omitempty" json:"reasoning_effort_on_none,omitempty"`
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
	// MaxStreamMinutes limits each upstream streaming attempt (minutes, default 3).
	// It cancels the upstream context, not all retries or a blocked downstream write.
	MaxStreamMinutes int `yaml:"max_stream_minutes" json:"max_stream_minutes"`
	// RateLimitCooldown 按 provider 的 retry 配置重试耗尽后仍返回 429 时的可选冷却秒数。
	// 默认关闭（<=0）；显式设置正数后，冷却期内后续请求跳过该 provider。
	RateLimitCooldown int `yaml:"rate_limit_cooldown" json:"rate_limit_cooldown"`
	// EntitlementCooldown 账号套餐额度耗尽（"token plan entitlement exhausted"）时的
	// 停用秒数。这类 429 与普通 rps/rpm/tpm 限流共用 code "8"，只能靠报文文字区分：
	// 它不随限流窗口滑动恢复，而是按厂商计费周期回补，所以既不该重试，也不该只停
	// rate_limit_cooldown 那么短。未设置（0）时默认 300 秒；设为负数则关闭该特殊
	// 处理，退回普通 429 行为。
	EntitlementCooldown int `yaml:"entitlement_cooldown" json:"entitlement_cooldown"`
}

type RetryConfig struct {
	// MaxRetries excludes the initial attempt; omitted/zero defaults to 3.
	MaxRetries    int     `yaml:"max_retries" json:"max_retries"`
	RetryInterval int     `yaml:"retry_interval" json:"retry_interval"`
	BackoffFactor float64 `yaml:"backoff_factor" json:"backoff_factor"`
}

type RateLimitConfig struct {
	Enabled bool    `yaml:"enabled" json:"enabled"`
	RPM     float64 `yaml:"rpm" json:"rpm"`     // requests per minute
	Burst   int     `yaml:"burst" json:"burst"` // max burst
}

type Provider struct {
	Name      string          `yaml:"name" json:"name"`
	Vendor    string          `yaml:"vendor,omitempty" json:"vendor,omitempty"`
	ModelID   string          `yaml:"model_id" json:"model_id"`
	APIKey    string          `yaml:"api_key" json:"api_key"`
	BaseURL   string          `yaml:"base_url" json:"base_url"`
	Priority  int             `yaml:"priority" json:"priority"`
	Retry     RetryConfig     `yaml:"retry" json:"retry"`
	Timeout   int             `yaml:"timeout" json:"timeout"` // seconds per non-streaming call or streaming header/read-idle wait
	Format    string          `yaml:"format" json:"format"`
	AuthType  string          `yaml:"auth_type" json:"auth_type"` // "bearer" or "x-api-key"; auto-detect from format if empty
	RateLimit RateLimitConfig `yaml:"rate_limit" json:"rate_limit"`
}

type ModelRoute struct {
	Alias  string `yaml:"alias" json:"alias"`
	Target string `yaml:"target" json:"target"`
}

type ModelRule struct {
	Model    string                 `yaml:"model" json:"model"`
	Defaults map[string]interface{} `yaml:"defaults" json:"defaults"`
}

// Package config 负责解析并校验服务配置。
//
// 优先级为：默认值 → 配置文件 → 环境变量。Load 返回一个不可变快照，
// 不使用全局变量，也不会把生效配置写回磁盘（旧实现会泄露密钥）。
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 投递通道取值。
const (
	ProviderCustom     = "custom"
	ProviderEnterprise = "enterprise"
)

// 限流策略取值。
const (
	StrategyWait   = "wait"
	StrategyReject = "reject"
)

// 出站限流的上限按通道取值，两个数字都来自各自的官方口径而不是同一个：
// 自定义机器人是硬限制（每机器人每分钟 20 条，超限封禁 10 分钟）；企业通道走
// 服务端 API 的基础频率限制，分钟级阈值为每应用每接口 1500 次（另有 40 次/秒
// 与标准版组织 20 QPS 的权益口径，实际配额可能更低）。
const (
	maxPerMinuteCustom     = 20
	maxPerMinuteEnterprise = 1500
)

// Duration 让 YAML 可以用 "5s" 这类可读写法表达时长。
type Duration time.Duration

// UnmarshalYAML 实现 yaml.Unmarshaler。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("duration must be a string such as \"5s\": %w", err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std 返回标准库时长。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String 实现 fmt.Stringer。
func (d Duration) String() string { return time.Duration(d).String() }

// Config 是服务的完整配置。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Provider  string          `yaml:"provider"`
	DingTalk  DingTalkConfig  `yaml:"dingtalk"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Send      SendConfig      `yaml:"send"`
	Log       LogConfig       `yaml:"log"`
	Source    string          `yaml:"-"`
}

// ServerConfig 是 HTTP 入口配置。
type ServerConfig struct {
	Addr                 string   `yaml:"addr"`
	APIToken             string   `yaml:"api_token"`
	AllowUnauthenticated bool     `yaml:"allow_unauthenticated"`
	MaxBodyBytes         int64    `yaml:"max_body_bytes"`
	ReadHeaderTimeout    Duration `yaml:"read_header_timeout"`
	ReadTimeout          Duration `yaml:"read_timeout"`
	WriteTimeout         Duration `yaml:"write_timeout"`
	IdleTimeout          Duration `yaml:"idle_timeout"`
	ShutdownTimeout      Duration `yaml:"shutdown_timeout"`
}

// DingTalkConfig 汇总两条投递通道的配置。
type DingTalkConfig struct {
	Custom     CustomConfig     `yaml:"custom"`
	Enterprise EnterpriseConfig `yaml:"enterprise"`
}

// CustomConfig 是存量自定义机器人 Webhook 配置。Secret 为空表示该机器人未启用加签。
type CustomConfig struct {
	AccessToken string `yaml:"access_token"`
	Secret      string `yaml:"secret"`
}

// EnterpriseConfig 是企业内部应用机器人配置。
type EnterpriseConfig struct {
	ClientID           string `yaml:"client_id"`
	ClientSecret       string `yaml:"client_secret"`
	RobotCode          string `yaml:"robot_code"`
	OpenConversationID string `yaml:"open_conversation_id"`
	IgnoreAt           bool   `yaml:"ignore_at"`
}

// RateLimitConfig 是出站限流配置。
type RateLimitConfig struct {
	Enabled   bool    `yaml:"enabled"`
	PerMinute float64 `yaml:"per_minute"`
	Burst     int     `yaml:"burst"`
	Strategy  string  `yaml:"strategy"`
}

// SendConfig 是单次投递的超时与重试配置。
type SendConfig struct {
	Timeout     Duration `yaml:"timeout"`
	Retries     int      `yaml:"retries"`
	BackoffBase Duration `yaml:"backoff_base"`
}

// LogConfig 是日志配置。
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Defaults 返回带默认值的配置。
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Addr:              ":8080",
			MaxBodyBytes:      65536,
			ReadHeaderTimeout: Duration(5 * time.Second),
			ReadTimeout:       Duration(30 * time.Second),
			WriteTimeout:      Duration(30 * time.Second),
			IdleTimeout:       Duration(60 * time.Second),
			ShutdownTimeout:   Duration(15 * time.Second),
		},
		Provider: ProviderCustom,
		RateLimit: RateLimitConfig{
			Enabled:   true,
			PerMinute: 18,
			Burst:     3,
			Strategy:  StrategyWait,
		},
		Send: SendConfig{
			Timeout:     Duration(10 * time.Second),
			Retries:     2,
			BackoffBase: Duration(200 * time.Millisecond),
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// resolved 是"已经读入并覆盖完环境变量"的配置，加上它的校验结论。
//
// 把它与 fatalErr 分开，是为了让只关心某几个字段的调用方（探活）能复用同一份
// 解析结果，而不必接受"配置不足以启动"这个结论——那个结论只对 serve 有意义。
type resolved struct {
	cfg      Config
	problems []string
}

// resolve 读取配置文件与环境变量，返回解析结果但与可运行性解耦。
//
// 返回的 error 只表示"配置文件读不到或不是合法 YAML"；环境变量格式错误与业务
// 校验失败一律进 problems，由调用方决定是否致命。
func resolve(path string) (resolved, error) {
	cfg := Defaults()

	explicit := path != ""
	if !explicit {
		path = discover()
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return resolved{cfg: cfg}, fmt.Errorf("read config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return resolved{cfg: cfg}, fmt.Errorf("parse config file %q: %w", path, err)
		}
		cfg.Source = path
	} else if explicit {
		return resolved{cfg: cfg}, fmt.Errorf("config file %q does not exist", path)
	}

	problems := applyEnv(&cfg)
	problems = append(problems, cfg.problems()...)
	return resolved{cfg: cfg, problems: problems}, nil
}

// Load 读取配置文件与环境变量，返回已校验的配置。
//
// path 为空时按 ./config.yaml、./config/config.yaml、/config/config.yaml 顺序查找，
// 全部不存在视为"仅用环境变量"，不报错。显式传入的 path 不存在则报错。
func Load(path string) (*Config, error) {
	result, err := resolve(path)
	if err != nil {
		return nil, err
	}
	if len(result.problems) > 0 {
		return nil, formatProblems(result.problems)
	}
	return &result.cfg, nil
}

// discover 返回找到的第一个配置文件路径，找不到则返回空串。
func discover() string {
	candidates := []string{
		"config.yaml",
		"config/config.yaml",
		"/config/config.yaml",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// applyEnv 用环境变量覆盖配置，返回无法解析的取值。
//
// 容器部署往往只用环境变量，因此解析失败必须让启动失败，而不是静默保留默认值：
// 否则 RATE_LIMIT_PER_MINUTE=abc、SEND_TIMEOUT=10 这类笔误会在运行期以错误额度
// 工作，且没有任何提示。空字符串仍视为"未设置"。
func applyEnv(cfg *Config) []string {
	var problems []string
	check := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}

	// 入口
	envString(&cfg.Server.Addr, "SERVER_ADDR")
	if port, ok := os.LookupEnv("PORT"); ok && port != "" {
		cfg.Server.Addr = ":" + port
	}
	envString(&cfg.Server.APIToken, "SERVER_API_TOKEN", "API_TOKEN")
	check(envBool(&cfg.Server.AllowUnauthenticated, "SERVER_ALLOW_UNAUTHENTICATED"))
	check(envInt64(&cfg.Server.MaxBodyBytes, "SERVER_MAX_BODY_BYTES"))
	check(envDuration(&cfg.Server.ShutdownTimeout, "SERVER_SHUTDOWN_TIMEOUT"))
	check(envDuration(&cfg.Server.ReadHeaderTimeout, "SERVER_READ_HEADER_TIMEOUT"))

	// 通道：只识别当前规范名，不保留上一主版本的 ACCESS_TOKEN / SECRET 别名。
	envString(&cfg.Provider, "PROVIDER")
	envString(&cfg.DingTalk.Custom.AccessToken, "DINGTALK_ACCESS_TOKEN")
	envString(&cfg.DingTalk.Custom.Secret, "DINGTALK_SECRET")
	envString(&cfg.DingTalk.Enterprise.ClientID, "DINGTALK_CLIENT_ID")
	envString(&cfg.DingTalk.Enterprise.ClientSecret, "DINGTALK_CLIENT_SECRET")
	envString(&cfg.DingTalk.Enterprise.RobotCode, "DINGTALK_ROBOT_CODE")
	envString(&cfg.DingTalk.Enterprise.OpenConversationID, "DINGTALK_OPEN_CONVERSATION_ID")
	check(envBool(&cfg.DingTalk.Enterprise.IgnoreAt, "DINGTALK_IGNORE_AT"))

	// 限流与投递
	check(envBool(&cfg.RateLimit.Enabled, "RATE_LIMIT_ENABLED"))
	check(envFloat(&cfg.RateLimit.PerMinute, "RATE_LIMIT_PER_MINUTE"))
	check(envInt(&cfg.RateLimit.Burst, "RATE_LIMIT_BURST"))
	envString(&cfg.RateLimit.Strategy, "RATE_LIMIT_STRATEGY")
	check(envDuration(&cfg.Send.Timeout, "SEND_TIMEOUT"))
	check(envInt(&cfg.Send.Retries, "SEND_RETRIES"))
	check(envDuration(&cfg.Send.BackoffBase, "SEND_BACKOFF_BASE"))

	// 日志
	envString(&cfg.Log.Level, "LOG_LEVEL")
	envString(&cfg.Log.Format, "LOG_FORMAT")

	return problems
}

// perMinuteCeiling 返回通道允许的每分钟上限与判定依据。
//
// 未知通道返回 ok=false：那种配置已经在 provider 校验里报过错，不必重复。
func perMinuteCeiling(provider string) (ceiling float64, basis string, ok bool) {
	switch provider {
	case ProviderCustom:
		return maxPerMinuteCustom,
			"the custom robot API allows 20 messages per minute and bans the robot for 10 minutes above it", true
	case ProviderEnterprise:
		return maxPerMinuteEnterprise,
			"the enterprise robot API follows the server API rate limits: 1500 calls per minute per app and interface", true
	}
	return 0, "", false
}

// problems 收集全部校验问题，便于与环境变量解析错误合并后一次输出。
func (c *Config) problems() []string {
	var problems []string

	switch c.Provider {
	case ProviderCustom:
		if c.DingTalk.Custom.AccessToken == "" {
			problems = append(problems, "dingtalk.custom.access_token is required when provider is \"custom\"")
		}
	case ProviderEnterprise:
		missing := make([]string, 0, 4)
		if c.DingTalk.Enterprise.ClientID == "" {
			missing = append(missing, "dingtalk.enterprise.client_id")
		}
		if c.DingTalk.Enterprise.ClientSecret == "" {
			missing = append(missing, "dingtalk.enterprise.client_secret")
		}
		if c.DingTalk.Enterprise.RobotCode == "" {
			missing = append(missing, "dingtalk.enterprise.robot_code")
		}
		if c.DingTalk.Enterprise.OpenConversationID == "" {
			missing = append(missing, "dingtalk.enterprise.open_conversation_id")
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("provider is \"enterprise\" but missing: %s", strings.Join(missing, ", ")))
		}
	default:
		problems = append(problems, fmt.Sprintf("provider must be %q or %q, got %q",
			ProviderCustom, ProviderEnterprise, c.Provider))
	}

	if c.Server.APIToken == "" && !c.Server.AllowUnauthenticated {
		problems = append(problems,
			"server.api_token is required unless server.allow_unauthenticated is true")
	}
	if c.Server.Addr == "" {
		problems = append(problems, "server.addr must not be empty")
	}
	if c.Server.MaxBodyBytes <= 0 {
		problems = append(problems, "server.max_body_bytes must be > 0")
	}
	durations := map[string]Duration{
		"server.read_header_timeout": c.Server.ReadHeaderTimeout,
		"server.read_timeout":        c.Server.ReadTimeout,
		"server.write_timeout":       c.Server.WriteTimeout,
		"server.idle_timeout":        c.Server.IdleTimeout,
		"server.shutdown_timeout":    c.Server.ShutdownTimeout,
		"send.timeout":               c.Send.Timeout,
	}
	for _, name := range sortedKeys(durations) {
		if durations[name] <= 0 {
			problems = append(problems, name+" must be > 0")
		}
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.PerMinute <= 0 {
			problems = append(problems, "rate_limit.per_minute must be > 0")
		}
		if ceiling, basis, ok := perMinuteCeiling(c.Provider); ok && c.RateLimit.PerMinute > ceiling {
			problems = append(problems, fmt.Sprintf(
				"rate_limit.per_minute must not exceed %v for provider %q: %s",
				ceiling, c.Provider, basis))
		}
		if c.RateLimit.Burst < 1 {
			problems = append(problems, "rate_limit.burst must be >= 1")
		}
		switch c.RateLimit.Strategy {
		case StrategyWait, StrategyReject:
		default:
			problems = append(problems,
				fmt.Sprintf("rate_limit.strategy must be %q or %q, got %q",
					StrategyWait, StrategyReject, c.RateLimit.Strategy))
		}
	}

	if c.Send.Retries < 0 || c.Send.Retries > 5 {
		problems = append(problems, "send.retries must be between 0 and 5")
	}

	if c.Send.BackoffBase < 0 {
		problems = append(problems, "send.backoff_base must be >= 0")
	}

	if _, err := parseLevel(c.Log.Level); err != nil {
		problems = append(problems, err.Error())
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		problems = append(problems, fmt.Sprintf("log.format must be \"json\" or \"text\", got %q", c.Log.Format))
	}

	return problems
}

// formatProblems 把多条问题渲染成一条错误，使运维一次修完。
func formatProblems(problems []string) error {
	return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
}

// parseLevel 解析日志级别。
func parseLevel(level string) (slog.Level, error) {
	if level == "" {
		return slog.LevelInfo, nil
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return 0, fmt.Errorf("log.level must be debug, info, warn or error, got %q", level)
	}
	return l, nil
}

// Level 返回解析后的日志级别。
func (c *Config) Level() slog.Level {
	l, err := parseLevel(c.Log.Level)
	if err != nil {
		return slog.LevelInfo
	}
	return l
}

// Redacted 返回一份把凭据替换为掩码的副本，可安全写入日志。
func (c *Config) Redacted() Config {
	out := *c
	out.Server.APIToken = mask(out.Server.APIToken)
	out.DingTalk.Custom.AccessToken = mask(out.DingTalk.Custom.AccessToken)
	out.DingTalk.Custom.Secret = mask(out.DingTalk.Custom.Secret)
	out.DingTalk.Enterprise.ClientSecret = mask(out.DingTalk.Enterprise.ClientSecret)
	return out
}

func mask(secret string) string {
	if secret == "" {
		return ""
	}
	return "***"
}

// ListenAddr 返回 -healthcheck 子命令要访问的监听地址。
//
// 它与 serve 共用同一份解析结果，因此配置文件里改 server.addr 也会被跟随；
// 探活只回答"端口上有没有人应答"，所以这里忽略校验问题（凭据缺失等），
// 配置文件读不到或无法解析时回落到默认地址。
func ListenAddr(path string) string {
	result, err := resolve(path)
	if err != nil || result.cfg.Server.Addr == "" {
		return Defaults().Server.Addr
	}
	return result.cfg.Server.Addr
}

func sortedKeys(m map[string]Duration) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// lookupEnv 返回第一个已设置且非空的环境变量。
func lookupEnv(names ...string) (value, name string, ok bool) {
	for _, candidate := range names {
		if v, found := os.LookupEnv(candidate); found && v != "" {
			return v, candidate, true
		}
	}
	return "", "", false
}

// invalidEnv 构造一条指明变量名与期望格式的错误。
func invalidEnv(name, value, want string) error {
	return fmt.Errorf("environment variable %s=%q is not %s", name, value, want)
}

func envString(dst *string, names ...string) {
	if v, _, ok := lookupEnv(names...); ok {
		*dst = v
	}
}

func envBool(dst *bool, names ...string) error {
	raw, name, ok := lookupEnv(names...)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return invalidEnv(name, raw, "a boolean such as true or false")
	}
	*dst = parsed
	return nil
}

func envInt(dst *int, names ...string) error {
	raw, name, ok := lookupEnv(names...)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return invalidEnv(name, raw, "an integer")
	}
	*dst = parsed
	return nil
}

func envInt64(dst *int64, names ...string) error {
	raw, name, ok := lookupEnv(names...)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return invalidEnv(name, raw, "an integer")
	}
	*dst = parsed
	return nil
}

func envFloat(dst *float64, names ...string) error {
	raw, name, ok := lookupEnv(names...)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return invalidEnv(name, raw, "a number")
	}
	*dst = parsed
	return nil
}

func envDuration(dst *Duration, names ...string) error {
	raw, name, ok := lookupEnv(names...)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return invalidEnv(name, raw, `a duration such as "10s"`)
	}
	*dst = Duration(parsed)
	return nil
}

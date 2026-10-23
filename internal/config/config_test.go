package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeConfig 写入一个临时配置文件并返回路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `
provider: custom
server:
  api_token: token-from-file
dingtalk:
  custom:
    access_token: token-from-file
    secret: secret-from-file
`)

	t.Setenv("DINGTALK_ACCESS_TOKEN", "token-from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := cfg.DingTalk.Custom.AccessToken; got != "token-from-env" {
		t.Errorf("access_token = %q, want env value", got)
	}
	if got := cfg.Server.APIToken; got != "token-from-file" {
		t.Errorf("api_token = %q, want value from file", got)
	}
	if got := cfg.DingTalk.Custom.Secret; got != "secret-from-file" {
		t.Errorf("secret = %q, want value from file", got)
	}
	if cfg.Source != path {
		t.Errorf("Source = %q, want %q", cfg.Source, path)
	}
}

func TestLegacyEnvNamesAreIgnored(t *testing.T) {
	path := writeConfig(t, "provider: custom\nserver:\n  api_token: t\n")

	// 上一主版本的变量名不再被读取，因此必须退化为"缺少必填项"。
	t.Setenv("ACCESS_TOKEN", "legacy-token")
	t.Setenv("SECRET", "legacy-secret")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want validation failure because legacy env names are ignored")
	}
	if !strings.Contains(err.Error(), "dingtalk.custom.access_token") {
		t.Errorf("error %q does not point at the missing access_token", err.Error())
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DINGTALK_ACCESS_TOKEN", "token")
	t.Setenv("SERVER_API_TOKEN", "api-token")

	cfg, err := Load(writeConfig(t, "provider: custom\n"))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Server.Addr != ":8080" {
		t.Errorf("addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Server.MaxBodyBytes != 65536 {
		t.Errorf("max_body_bytes = %d, want 65536", cfg.Server.MaxBodyBytes)
	}
	if cfg.RateLimit.PerMinute != 18 {
		t.Errorf("per_minute = %v, want 18", cfg.RateLimit.PerMinute)
	}
	if cfg.RateLimit.Strategy != StrategyWait {
		t.Errorf("strategy = %q, want %q", cfg.RateLimit.Strategy, StrategyWait)
	}
	if cfg.Send.Retries != 2 {
		t.Errorf("retries = %d, want 2", cfg.Send.Retries)
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		env      map[string]string
		wantErrs []string
	}{
		{
			name:     "custom provider without access token",
			yaml:     "provider: custom\nserver:\n  api_token: t\n",
			wantErrs: []string{"dingtalk.custom.access_token"},
		},
		{
			name:     "enterprise provider missing credentials",
			yaml:     "provider: enterprise\nserver:\n  api_token: t\n",
			wantErrs: []string{"client_id", "client_secret", "robot_code", "open_conversation_id"},
		},
		{
			name:     "missing inbound token and anonymous not allowed",
			yaml:     "provider: custom\ndingtalk:\n  custom:\n    access_token: a\n",
			wantErrs: []string{"server.api_token"},
		},
		{
			name:     "unknown provider",
			yaml:     "provider: telegram\nserver:\n  api_token: t\n",
			wantErrs: []string{`provider must be`},
		},
		{
			name:     "custom rate limit above the custom robot ceiling",
			yaml:     "provider: custom\nserver:\n  api_token: t\ndingtalk:\n  custom:\n    access_token: a\nrate_limit:\n  per_minute: 25\n",
			wantErrs: []string{"rate_limit.per_minute", `for provider "custom"`},
		},
		{
			name:     "enterprise rate limit above the server API ceiling",
			yaml:     "provider: enterprise\nserver:\n  api_token: t\ndingtalk:\n  enterprise:\n    client_id: i\n    client_secret: s\n    robot_code: r\n    open_conversation_id: c\nrate_limit:\n  per_minute: 1501\n",
			wantErrs: []string{"rate_limit.per_minute", `for provider "enterprise"`},
		},
		{
			name:     "invalid log level",
			yaml:     "provider: custom\nserver:\n  api_token: t\ndingtalk:\n  custom:\n    access_token: a\nlog:\n  level: verbose\n",
			wantErrs: []string{"log.level"},
		},
		{
			name:     "invalid duration",
			yaml:     "provider: custom\nserver:\n  api_token: t\n  shutdown_timeout: soon\n",
			wantErrs: []string{"invalid duration"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := Load(writeConfig(t, tc.yaml))
			if err == nil {
				t.Fatal("Load returned nil error, want validation failure")
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestLoadRateLimitCeilingPerProvider(t *testing.T) {
	const customBase = "provider: custom\nserver:\n  api_token: t\ndingtalk:\n  custom:\n    access_token: a\n"
	const enterpriseBase = "provider: enterprise\nserver:\n  api_token: t\ndingtalk:\n  enterprise:\n    client_id: i\n    client_secret: s\n    robot_code: r\n    open_conversation_id: c\n"

	tests := []struct {
		name     string
		base     string
		rate     string
		wantErrs []string
	}{
		{
			name: "custom at its own ceiling",
			base: customBase,
			rate: "enabled: true\n  per_minute: 20",
		},
		{
			name:     "custom above its own ceiling",
			base:     customBase,
			rate:     "enabled: true\n  per_minute: 21",
			wantErrs: []string{"rate_limit.per_minute", `for provider "custom"`, "20"},
		},
		{
			name: "enterprise above the custom robot ceiling",
			base: enterpriseBase,
			rate: "enabled: true\n  per_minute: 60",
		},
		{
			name: "enterprise at the server API ceiling",
			base: enterpriseBase,
			rate: "enabled: true\n  per_minute: 1500",
		},
		{
			name:     "enterprise above the server API ceiling",
			base:     enterpriseBase,
			rate:     "enabled: true\n  per_minute: 1501",
			wantErrs: []string{"rate_limit.per_minute", `for provider "enterprise"`, "1500"},
		},
		{
			name: "disabled rate limit ignores the ceiling",
			base: customBase,
			rate: "enabled: false\n  per_minute: 100",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.base+"rate_limit:\n  "+tc.rate+"\n"))
			if len(tc.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("Load returned %v, want the configuration accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Load returned nil error, want a ceiling violation")
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

// validConfigYAML 是能通过全部校验的最小配置。
const validConfigYAML = `
provider: custom
server:
  api_token: t
dingtalk:
  custom:
    access_token: a
`

func TestLoadRejectsUnparsableEnv(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "non numeric rate", key: "RATE_LIMIT_PER_MINUTE", value: "abc"},
		{name: "duration without unit", key: "SEND_TIMEOUT", value: "10"},
		{name: "non numeric burst", key: "RATE_LIMIT_BURST", value: "x"},
		{name: "non boolean flag", key: "RATE_LIMIT_ENABLED", value: "yes"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)

			_, err := Load(writeConfig(t, validConfigYAML))
			if err == nil {
				t.Fatalf("Load accepted %s=%q, want a startup failure", tc.key, tc.value)
			}
			for _, want := range []string{"environment variable", tc.key, tc.value} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestLoadReportsEnvAndValidationProblemsTogether(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "abc")

	_, err := Load(writeConfig(t, "provider: custom\nserver:\n  api_token: t\n"))
	if err == nil {
		t.Fatal("Load returned nil error, want both problems reported")
	}
	for _, want := range []string{"environment variable RATE_LIMIT_PER_MINUTE", "dingtalk.custom.access_token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestLoadTreatsEmptyEnvAsUnset(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("SEND_TIMEOUT", "")

	cfg, err := Load(writeConfig(t, validConfigYAML))
	if err != nil {
		t.Fatalf("Load returned error for empty env values: %v", err)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("log.level = %q, want the default info", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("log.format = %q, want the default text", cfg.Log.Format)
	}
	if cfg.Send.Timeout.Std() != 10*time.Second {
		t.Errorf("send.timeout = %s, want the default 10s", cfg.Send.Timeout)
	}
}

func TestLoadAllowsUnauthenticatedWhenExplicit(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
provider: custom
server:
  allow_unauthenticated: true
dingtalk:
  custom:
    access_token: a
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.APIToken != "" {
		t.Errorf("api_token = %q, want empty", cfg.Server.APIToken)
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
provider: enterprise
server:
  api_token: api-secret-value
dingtalk:
  custom:
    access_token: custom-token-value
    secret: custom-secret-value
  enterprise:
    client_id: client-id-value
    client_secret: client-secret-value
    robot_code: robot-code-value
    open_conversation_id: conversation-value
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	rendered := renderConfig(cfg.Redacted())
	for _, secret := range []string{"api-secret-value", "custom-token-value", "custom-secret-value", "client-secret-value"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("redacted config still contains %q", secret)
		}
	}
	for _, keep := range []string{"client-id-value", "robot-code-value"} {
		if !strings.Contains(rendered, keep) {
			t.Errorf("redacted config unexpectedly hides %q", keep)
		}
	}
}

// TestExampleConfigUsesKnownFields 防止示例配置与配置结构漂移。
//
// yaml.Unmarshal 默认忽略未知键，若某个键被改名或删除而示例未同步，
// 示例文件里会留下"看起来有效、实际被忽略"的配置项。这里用严格模式解码，
// 任何多余键都会让测试失败。
func TestExampleConfigUsesKnownFields(t *testing.T) {
	path := filepath.Join("..", "..", "config", "example.config.yaml")

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		t.Fatalf("%s contains keys the Config struct does not recognise: %v", path, err)
	}

	if cfg.Provider == "" {
		t.Error("example config does not set provider")
	}
}

// writeFileAt 在目录下按相对路径写入文件，用于构造配置文件发现场景。
func writeFileAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestLoadDiscoversPreferredConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFileAt(t, dir, "config.yaml", "provider: custom\nserver:\n  api_token: from-root\ndingtalk:\n  custom:\n    access_token: tok\n")
	writeFileAt(t, dir, filepath.Join("config", "config.yaml"), "provider: custom\nserver:\n  api_token: from-nested\ndingtalk:\n  custom:\n    access_token: tok\n")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source != "config.yaml" {
		t.Errorf("Source = %q, want %q: ./config.yaml must win over ./config/config.yaml", cfg.Source, "config.yaml")
	}
	if cfg.Server.APIToken != "from-root" {
		t.Errorf("api token = %q, want %q", cfg.Server.APIToken, "from-root")
	}
}

func TestLoadDiscoversNestedConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFileAt(t, dir, filepath.Join("config", "config.yaml"), "provider: custom\nserver:\n  api_token: from-nested\ndingtalk:\n  custom:\n    access_token: tok\n")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join("config", "config.yaml"); cfg.Source != want {
		t.Errorf("Source = %q, want %q", cfg.Source, want)
	}
	if cfg.Server.APIToken != "from-nested" {
		t.Errorf("api token = %q, want %q", cfg.Server.APIToken, "from-nested")
	}
}

func TestListenAddrFollowsConfigFile(t *testing.T) {
	// 探活与 serve 用同一份解析结果：地址写在文件里也必须被跟随。
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", "")

	path := writeConfig(t, "server:\n  addr: \":8081\"\n")
	if got := ListenAddr(path); got != ":8081" {
		t.Errorf("ListenAddr = %q, want the address from the config file", got)
	}
}

func TestListenAddrPortEnvOverridesConfigFile(t *testing.T) {
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", "9090")

	path := writeConfig(t, "server:\n  addr: \":8081\"\n")
	if got := ListenAddr(path); got != ":9090" {
		t.Errorf("ListenAddr = %q, want PORT to win over the config file", got)
	}
}

func TestListenAddrToleratesUnusableConfig(t *testing.T) {
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", "")

	tests := []struct {
		name    string
		content string
	}{
		{name: "unsupported yaml", content: "server: [oops\n"},
		{name: "missing credentials", content: "provider: enterprise\n"},
		{name: "empty address", content: "server:\n  addr: \"\"\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 探活只回答"端口上有没有人应答"，配置不足以启动不该让它失败。
			if got := ListenAddr(writeConfig(t, tc.content)); got != ":8080" {
				t.Errorf("ListenAddr = %q, want the default :8080", got)
			}
		})
	}
}

func TestListenAddrFallsBackWhenExplicitFileIsMissing(t *testing.T) {
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", "")

	if got := ListenAddr("/nonexistent/config.yaml"); got != ":8080" {
		t.Errorf("ListenAddr = %q, want the default :8080", got)
	}
}

func TestLoadWithoutConfigFileUsesEnvironmentOnly(t *testing.T) {
	if _, err := os.Stat("/config/config.yaml"); err == nil {
		t.Skip("host provides /config/config.yaml, which the discovery order checks last")
	}
	t.Chdir(t.TempDir())
	t.Setenv("API_TOKEN", "token-from-env")
	t.Setenv("DINGTALK_ACCESS_TOKEN", "access-token-from-env")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned an error when only environment variables were provided: %v", err)
	}
	if cfg.Source != "" {
		t.Errorf("Source = %q, want empty: no config file exists", cfg.Source)
	}
	if cfg.Server.APIToken != "token-from-env" {
		t.Errorf("api token = %q, want %q", cfg.Server.APIToken, "token-from-env")
	}
}

func TestLoadMissingExplicitFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("Load returned nil error for a missing explicit path")
	}
}

func TestLoadBrokenYAMLFails(t *testing.T) {
	if _, err := Load(writeConfig(t, "provider: [unclosed\n")); err == nil {
		t.Fatal("Load returned nil error for malformed YAML")
	}
}

// renderConfig 用 %+v 渲染配置，用于断言脱敏效果。
func renderConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString(cfg.Server.APIToken)
	b.WriteString(cfg.DingTalk.Custom.AccessToken)
	b.WriteString(cfg.DingTalk.Custom.Secret)
	b.WriteString(cfg.DingTalk.Enterprise.ClientID)
	b.WriteString(cfg.DingTalk.Enterprise.ClientSecret)
	b.WriteString(cfg.DingTalk.Enterprise.RobotCode)
	b.WriteString(cfg.DingTalk.Enterprise.OpenConversationID)
	return b.String()
}

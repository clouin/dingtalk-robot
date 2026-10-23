package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dingtalk-robot/internal/config"
)

// writeTempConfig 写入一个临时配置文件并返回路径。
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run([]string{"-version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Errorf("stdout = %q, want %q", got, version)
	}
}

func TestRunHealthcheckSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	t.Setenv("SERVER_ADDR", server.Listener.Addr().String())

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-healthcheck"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
}

func TestRunHealthcheckReportsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.Listener.Addr().String()
	server.Close()
	t.Setenv("SERVER_ADDR", addr)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-healthcheck"}, &stdout, &stderr); code == 0 {
		t.Fatalf("exit code = 0, want non-zero for an unreachable health endpoint")
	}
	if !strings.Contains(stderr.String(), "healthcheck failed") {
		t.Errorf("stderr = %q, want it to mention the healthcheck failure", stderr.String())
	}
}

func TestRunHealthcheckFollowsConfigFileAddr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	// 地址只写在配置文件里，环境变量留空：探活必须跟随文件。
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", "")

	path := writeTempConfig(t, "server:\n  addr: \""+server.Listener.Addr().String()+"\"\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-healthcheck", "-config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
}

func TestRunHealthcheckPortEnvOverridesConfigFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("PORT", port)

	// 文件里的地址是死的，只有环境变量生效才可能探活成功。
	path := writeTempConfig(t, "server:\n  addr: \"127.0.0.1:1\"\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-healthcheck", "-config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 because PORT overrides the config file (stderr=%q)", code, stderr.String())
	}
}

func TestRunFailsOnUnreadableConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run([]string{"-config", "/nonexistent/config.yaml"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "fatal:") {
		t.Errorf("stderr = %q, want it to report a fatal error", stderr.String())
	}
}

func TestBuildProviderSelectsChannel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name      string
		configure func(*config.Config)
		wantName  string
		wantErr   bool
	}{
		{
			name: "custom channel",
			configure: func(c *config.Config) {
				c.Provider = config.ProviderCustom
				c.DingTalk.Custom.AccessToken = "tok"
				c.DingTalk.Custom.Secret = "secret"
			},
			wantName: "custom",
		},
		{
			name: "enterprise channel",
			configure: func(c *config.Config) {
				c.Provider = config.ProviderEnterprise
				c.DingTalk.Enterprise.ClientID = "id"
				c.DingTalk.Enterprise.ClientSecret = "secret"
				c.DingTalk.Enterprise.RobotCode = "robot"
				c.DingTalk.Enterprise.OpenConversationID = "cid"
			},
			wantName: "enterprise",
		},
		{
			name: "unknown channel",
			configure: func(c *config.Config) {
				c.Provider = "telegram"
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			tc.configure(&cfg)

			prov, err := buildProvider(&cfg, logger)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("buildProvider returned no error for provider %q", cfg.Provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildProvider: %v", err)
			}
			if got := prov.Name(); got != tc.wantName {
				t.Errorf("provider name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

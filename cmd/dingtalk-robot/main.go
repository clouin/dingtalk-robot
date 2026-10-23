// Command dingtalk-robot 是一个把入站 Webhook 转发到钉钉群的单容器服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"dingtalk-robot/internal/app"
	"dingtalk-robot/internal/config"
	"dingtalk-robot/internal/httpapi"
	"dingtalk-robot/internal/observability"
	"dingtalk-robot/internal/provider"
	"dingtalk-robot/internal/provider/custom"
	"dingtalk-robot/internal/provider/enterprise"
	"dingtalk-robot/internal/relay"
)

// version 是构建版本，发布时可用 -ldflags "-X main.version=..." 覆盖。
var version = "2.0.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 解析参数并执行命令，返回进程退出码。输出流由调用方注入，便于测试。
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("dingtalk-robot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to the YAML configuration file")
	healthcheck := flags.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	showVersion := flags.Bool("version", false, "print version and exit")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if *healthcheck {
		if err := app.CheckHealth(context.Background(), config.ListenAddr(*configPath), 2*time.Second); err != nil {
			fmt.Fprintf(stderr, "healthcheck failed: %v\n", err)
			return 1
		}
		return 0
	}

	if err := serve(*configPath); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	return 0
}

func serve(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := observability.SetupLogger(cfg.Level(), cfg.Log.Format)

	// 默认级别只保留一行摘要；完整生效配置降为 debug，避免每次启动刷出一大段
	// 结构体转储（其中多数字段与默认值相同）。
	startup := []any{"version", version}
	if cfg.Source != "" {
		startup = append(startup, "source", cfg.Source)
	}
	logger.Info("starting", startup...)
	logger.Debug("configuration loaded", "config", cfg.Redacted())

	prov, err := buildProvider(cfg, logger)
	if err != nil {
		return err
	}

	metrics := observability.NewMetrics()

	var limiter *rate.Limiter
	if cfg.RateLimit.Enabled {
		limiter = rate.NewLimiter(rate.Limit(cfg.RateLimit.PerMinute/60), cfg.RateLimit.Burst)
	}

	sender := relay.New(relay.Config{
		Provider:    prov,
		RateLimit:   limiter,
		Strategy:    cfg.RateLimit.Strategy,
		Retries:     cfg.Send.Retries,
		BackoffBase: cfg.Send.BackoffBase.Std(),
		Timeout:     cfg.Send.Timeout.Std(),
		Metrics:     metrics,
		Logger:      logger,
	})

	readiness := app.NewReadiness()
	handler := httpapi.NewRouter(httpapi.Deps{
		Relay:        sender,
		APIToken:     cfg.Server.APIToken,
		MaxBodyBytes: cfg.Server.MaxBodyBytes,
		Ready:        readiness.Ready,
		Metrics:      metrics,
		Logger:       logger,
	})

	instance, err := app.New(cfg, handler, logger, readiness)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("listening",
		"addr", instance.Addr().String(),
		"provider", prov.Name(),
	)

	if err := instance.Run(ctx); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}

func buildProvider(cfg *config.Config, logger *slog.Logger) (provider.Provider, error) {
	switch cfg.Provider {
	case config.ProviderCustom:
		if cfg.DingTalk.Custom.Secret == "" {
			logger.Warn("no dingtalk secret configured: requests are sent without a signature")
		}
		return custom.New(custom.Config{
			AccessToken: cfg.DingTalk.Custom.AccessToken,
			Secret:      cfg.DingTalk.Custom.Secret,
			Client:      provider.NewHTTPClient(cfg.Send.Timeout.Std()),
		}), nil

	case config.ProviderEnterprise:
		if cfg.DingTalk.Enterprise.IgnoreAt {
			logger.Warn("enterprise robot does not support @: at mentions will be dropped")
		}
		return enterprise.New(enterprise.Config{
			ClientID:           cfg.DingTalk.Enterprise.ClientID,
			ClientSecret:       cfg.DingTalk.Enterprise.ClientSecret,
			RobotCode:          cfg.DingTalk.Enterprise.RobotCode,
			OpenConversationID: cfg.DingTalk.Enterprise.OpenConversationID,
			IgnoreAt:           cfg.DingTalk.Enterprise.IgnoreAt,
			Client:             provider.NewHTTPClient(cfg.Send.Timeout.Std()),
		}), nil
	}

	return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
}

// Package app 负责进程生命周期：监听、就绪状态与优雅停机。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"dingtalk-robot/internal/config"
)

// Readiness 是 HTTP 入口与停机流程共享的就绪开关。
type Readiness struct {
	ready atomic.Bool
}

// NewReadiness 创建就绪开关。
func NewReadiness() *Readiness { return &Readiness{} }

// Set 设置就绪状态。
func (r *Readiness) Set(value bool) { r.ready.Store(value) }

// Ready 返回当前就绪状态。
func (r *Readiness) Ready() bool { return r.ready.Load() }

// App 是承载 HTTP 服务的进程。
type App struct {
	cfg       config.Config
	log       *slog.Logger
	listener  net.Listener
	server    *http.Server
	readiness *Readiness
}

// New 创建 App 并绑定监听地址。
func New(cfg *config.Config, handler http.Handler, logger *slog.Logger, readiness *Readiness) (*App, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", cfg.Server.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", cfg.Server.Addr, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
		ReadTimeout:       cfg.Server.ReadTimeout.Std(),
		WriteTimeout:      cfg.Server.WriteTimeout.Std(),
		IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	return &App{
		cfg:       *cfg,
		log:       logger,
		listener:  listener,
		server:    server,
		readiness: readiness,
	}, nil
}

// Addr 返回实际监听地址。
func (a *App) Addr() net.Addr { return a.listener.Addr() }

// Run 启动服务并阻塞，直到 ctx 被取消或服务出错。
//
// 收到取消信号后先置为未就绪（让探针与负载均衡停止转发），再排空在途请求；
// 排空超过 shutdown_timeout 时返回错误，由调用方以非零状态退出。
func (a *App) Run(ctx context.Context) error {
	a.readiness.Set(true)

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(a.listener) }()

	select {
	case err := <-serveErr:
		a.readiness.Set(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server stopped unexpectedly: %w", err)

	case <-ctx.Done():
		a.readiness.Set(false)
		a.log.Info("graceful shutdown started",
			"reason", context.Cause(ctx),
			"timeout", a.cfg.Server.ShutdownTimeout.String(),
		)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout.Std())
		defer cancel()

		if err := a.server.Shutdown(shutdownCtx); err != nil {
			_ = a.server.Close()
			return fmt.Errorf("requests did not drain within %s: %w", a.cfg.Server.ShutdownTimeout, err)
		}

		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server stopped unexpectedly: %w", err)
		}
		return nil
	}
}

// CheckHealth 请求存活探针，供 -healthcheck 子命令使用。
//
// addr 是服务的监听地址；通配地址（空 host、0.0.0.0、::）回退到回环地址。
func CheckHealth(ctx context.Context, addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthcheckURL(addr), nil)
	if err != nil {
		return err
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// healthcheckURL 把监听地址转换为探活 URL。
func healthcheckURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "127.0.0.1", "8080"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

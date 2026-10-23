package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"dingtalk-robot/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	return &cfg
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunDrainsInFlightRequests(t *testing.T) {
	cfg := testConfig(t)
	readiness := NewReadiness()

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusOK)
	})

	instance, err := New(cfg, handler, discardLogger(), readiness)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- instance.Run(ctx) }()

	statusCh := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + instance.Addr().String() + "/v1/messages") //nolint:noctx // 测试用短请求
		if err != nil {
			statusCh <- -1
			return
		}
		defer func() { _ = resp.Body.Close() }()
		statusCh <- resp.StatusCode
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}

	if !readiness.Ready() {
		t.Fatal("app should report ready before shutdown")
	}

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for readiness.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("readiness never flipped to false after cancellation")
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(release)

	select {
	case code := <-statusCh:
		if code != http.StatusOK {
			t.Errorf("in-flight request status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not complete")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
}

func TestRunRejectsNewConnectionsAfterShutdown(t *testing.T) {
	cfg := testConfig(t)
	readiness := NewReadiness()

	instance, err := New(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), discardLogger(), readiness)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	addr := instance.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- instance.Run(ctx) }()

	// 等到服务真的在监听。
	waitReachable(t, addr)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Error("listener still accepts connections after shutdown")
	}
}

func TestNewFailsOnInvalidAddr(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.Addr = "127.0.0.1:99999"

	if _, err := New(cfg, http.NotFoundHandler(), discardLogger(), NewReadiness()); err == nil {
		t.Fatal("New returned nil error for an invalid listen address")
	}
}

func TestCheckHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := CheckHealth(context.Background(), "127.0.0.1:"+portOf(t, server.URL), time.Second); err != nil {
		t.Errorf("CheckHealth returned error for healthy server: %v", err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	if err := CheckHealth(context.Background(), "127.0.0.1:"+portOf(t, broken.URL), time.Second); err == nil {
		t.Error("CheckHealth returned nil error for unhealthy server")
	}
}

func TestHealthcheckURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "port only", addr: ":8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "wildcard ipv4", addr: "0.0.0.0:9090", want: "http://127.0.0.1:9090/healthz"},
		{name: "wildcard ipv6", addr: "[::]:8081", want: "http://127.0.0.1:8081/healthz"},
		{name: "explicit host", addr: "192.0.2.10:8080", want: "http://192.0.2.10:8080/healthz"},
		{name: "loopback", addr: "127.0.0.1:8082", want: "http://127.0.0.1:8082/healthz"},
		{name: "malformed", addr: "nonsense", want: "http://127.0.0.1:8080/healthz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthcheckURL(tc.addr); got != tc.want {
				t.Errorf("healthcheckURL(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func waitReachable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("server at %s never became reachable", addr)
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(rawURL[len("http://"):])
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	return port
}

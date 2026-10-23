package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/observability"
)

// withLogger 把访问日志写到测试缓冲区，便于断言日志内容。
func withLogger(logger *slog.Logger) option {
	return func(d *Deps, _ *stubProvider) { d.Logger = logger }
}

// logChain 按 router.go 的顺序手工组装中间件链，供注入会 panic 的处理函数。
func logChain(s *server, route http.Handler) http.Handler {
	handler := route
	handler = s.withAuth(handler)
	handler = s.withRecover(handler)
	handler = s.withMetrics(handler)
	handler = s.withAccessLog(handler)
	return s.withRequestID(handler)
}

// scrapeRequestsTotal 抓取 /metrics，返回 requests_total 的完整文本，供断言计数。
func scrapeRequestsTotal(t *testing.T, s *testServer) string {
	t.Helper()

	rec := s.do(t, http.MethodGet, "/metrics", "", authHeader("secret-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var lines []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "dingtalk_robot_requests_total{") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func TestAuthRejectionIsCounted(t *testing.T) {
	s := newTestServer(t, withMetrics(observability.NewMetrics()))

	if rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`,
		map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	got := scrapeRequestsTotal(t, s)
	want := `dingtalk_robot_requests_total{route="/v1/messages",status="401"} 1`
	if !strings.Contains(got, want) {
		t.Errorf("metrics %q missing %q", got, want)
	}
}

func TestAuthRejectionOnUnknownPathIsUnmatched(t *testing.T) {
	s := newTestServer(t, withMetrics(observability.NewMetrics()))

	if rec := s.do(t, http.MethodGet, "/nope", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	got := scrapeRequestsTotal(t, s)
	want := `dingtalk_robot_requests_total{route="unmatched",status="401"} 1`
	if !strings.Contains(got, want) {
		t.Errorf("metrics %q missing %q", got, want)
	}
}

func TestRejectionsAndPanicsAreCounted(t *testing.T) {
	metrics := observability.NewMetrics()

	s := newTestServer(t, withMetrics(metrics))
	s.do(t, http.MethodGet, "/v1/messages", "", authHeader("secret-token"))
	s.do(t, http.MethodGet, "/healthz", "", nil)
	s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))

	// panic 路径也要被计数：recover 在内层写回 500，metrics 在外层读到状态码。
	panicServer := &server{deps: Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: metrics,
	}}
	mux := http.NewServeMux()
	mux.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
	panicRec := httptest.NewRecorder()
	logChain(panicServer, mux).ServeHTTP(panicRec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if panicRec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", panicRec.Code)
	}

	got := scrapeRequestsTotal(t, s)
	for _, want := range []string{
		`dingtalk_robot_requests_total{route="/v1/messages",status="405"} 1`,
		`dingtalk_robot_requests_total{route="/healthz",status="200"} 1`,
		`dingtalk_robot_requests_total{route="/v1/messages",status="200"} 1`,
		`dingtalk_robot_requests_total{route="/boom",status="500"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metrics %q missing %q", got, want)
		}
	}
}

func TestRouteLabelCoversMiddlewareRejections(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		wantStatus int
		wantRoute  string
		level      slog.Level
	}{
		{
			name:       "auth rejection keeps the real route",
			method:     http.MethodPost,
			path:       "/v1/messages",
			headers:    map[string]string{"Content-Type": "application/json"},
			wantStatus: http.StatusUnauthorized,
			wantRoute:  "/v1/messages",
		},
		{
			name:       "method rejection keeps the real route",
			method:     http.MethodGet,
			path:       "/v1/messages",
			headers:    map[string]string{"Authorization": "Bearer secret-token"},
			wantStatus: http.StatusMethodNotAllowed,
			wantRoute:  "/v1/messages",
		},
		{
			name:       "unknown path is unmatched",
			method:     http.MethodGet,
			path:       "/nope",
			headers:    map[string]string{"Authorization": "Bearer secret-token"},
			wantStatus: http.StatusNotFound,
			wantRoute:  routeUnmatched,
		},
		{
			name:       "probe keeps its route",
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantRoute:  "/healthz",
			level:      slog.LevelDebug,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := newTestServer(t, withLogger(observability.NewLogger(&buf, tc.level, "text")))

			rec := s.do(t, tc.method, tc.path, "", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			line := buf.String()
			if !strings.Contains(line, "route="+tc.wantRoute) {
				t.Errorf("log %q does not carry route=%s", line, tc.wantRoute)
			}
			if strings.Contains(line, "route="+routeUnmatched) && tc.wantRoute != routeUnmatched {
				t.Errorf("log %q must not fall back to %s", line, routeUnmatched)
			}
		})
	}
}

func TestProbeRequestsStayOutOfInfoLog(t *testing.T) {
	var quiet bytes.Buffer
	s := newTestServer(t, withLogger(observability.NewLogger(&quiet, slog.LevelInfo, "text")))

	for i := 0; i < 3; i++ {
		if rec := s.do(t, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
			t.Fatalf("healthz status = %d", rec.Code)
		}
		if rec := s.do(t, http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusOK {
			t.Fatalf("readyz status = %d", rec.Code)
		}
	}

	if quiet.Len() != 0 {
		t.Errorf("probe requests produced info logs: %q", quiet.String())
	}

	var verbose bytes.Buffer
	s = newTestServer(t, withLogger(observability.NewLogger(&verbose, slog.LevelDebug, "text")))
	s.do(t, http.MethodGet, "/healthz", "", nil)

	if !strings.Contains(verbose.String(), "route=/healthz") {
		t.Errorf("debug level does not show the probe record: %q", verbose.String())
	}
}

func TestAccessLogOmitsEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(t, withLogger(observability.NewLogger(&buf, slog.LevelInfo, "text")))

	s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, map[string]string{"Content-Type": "application/json"})

	line := buf.String()
	for _, unwanted := range []string{"msgtype=", "provider=", "errcode=", "attempts="} {
		if strings.Contains(line, unwanted) {
			t.Errorf("rejected request log %q should omit %s", line, unwanted)
		}
	}

	buf.Reset()
	s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))

	delivered := buf.String()
	for _, want := range []string{"msgtype=text", "provider=stub", "attempts=1"} {
		if !strings.Contains(delivered, want) {
			t.Errorf("delivered request log %q missing %s", delivered, want)
		}
	}
	if strings.Contains(delivered, "errcode=") {
		t.Errorf("successful delivery should omit errcode: %q", delivered)
	}
}

func TestPanicBeforeWriteReturnsJSON500WithRequestID(t *testing.T) {
	var buf bytes.Buffer
	s := &server{deps: Deps{Logger: observability.NewLogger(&buf, slog.LevelInfo, "text")}}

	mux := http.NewServeMux()
	mux.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))

	rec := httptest.NewRecorder()
	logChain(s, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeInternal) {
		t.Errorf("body %s missing %s", rec.Body.String(), apierr.CodeInternal)
	}

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("X-Request-Id header missing")
	}
	logs := buf.String()
	if !strings.Contains(logs, "level=error") || !strings.Contains(logs, "panic while handling request") {
		t.Errorf("panic was not logged at error level: %q", logs)
	}
	if !strings.Contains(logs, "request_id="+requestID) {
		t.Errorf("panic log %q does not carry request_id=%s", logs, requestID)
	}
	if !strings.Contains(logs, "status=500") {
		t.Errorf("access log %q does not record the 500", logs)
	}
}

func TestPanicAfterWriteKeepsOriginalResponse(t *testing.T) {
	var buf bytes.Buffer
	s := &server{deps: Deps{Logger: observability.NewLogger(&buf, slog.LevelInfo, "text")}}

	mux := http.NewServeMux()
	mux.Handle("/partial", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late boom")
	}))

	rec := httptest.NewRecorder()
	logChain(s, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partial", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the original 200", rec.Code)
	}
	if got := rec.Body.String(); got != "partial" {
		t.Errorf("body = %q, want the already-written content untouched", got)
	}
	if strings.Contains(buf.String(), apierr.CodeInternal) {
		t.Errorf("log %q should not claim an internal error body was written", buf.String())
	}
}

func TestPanicKeepsServerServing(t *testing.T) {
	var buf bytes.Buffer
	s := &server{deps: Deps{Logger: observability.NewLogger(&buf, slog.LevelInfo, "text")}}

	mux := http.NewServeMux()
	mux.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
	mux.Handle("/ok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	handler := logChain(s, mux)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if second.Code != http.StatusOK || second.Body.String() != "ok" {
		t.Fatalf("second request = %d %q, want 200 ok", second.Code, second.Body.String())
	}
}

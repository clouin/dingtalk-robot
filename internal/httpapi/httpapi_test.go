package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/config"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/observability"
	"dingtalk-robot/internal/provider"
	"dingtalk-robot/internal/relay"
)

// stubProvider 返回预设结果并统计调用次数。
type stubProvider struct {
	class        provider.ErrorClass
	errCode      int64
	upstreamCode string
	errMsg       string
	retryAfter   time.Duration
	validateErr  error
	calls        atomic.Int32
	atDropped    bool
}

func (p *stubProvider) Name() string { return "stub" }

func (p *stubProvider) Validate(*message.Message) error { return p.validateErr }

func (p *stubProvider) Send(context.Context, *message.Message) provider.Result {
	p.calls.Add(1)
	return provider.Result{
		Class:        p.class,
		ErrCode:      p.errCode,
		UpstreamCode: p.upstreamCode,
		ErrMsg:       p.errMsg,
		RetryAfter:   p.retryAfter,
		AtDropped:    p.atDropped,
	}
}

type testServer struct {
	handler http.Handler
	stub    *stubProvider
}

type option func(*Deps, *stubProvider)

func withMaxBody(limit int64) option {
	return func(d *Deps, _ *stubProvider) { d.MaxBodyBytes = limit }
}

func withResult(class provider.ErrorClass, errCode int64) option {
	return func(_ *Deps, p *stubProvider) {
		p.class = class
		p.errCode = errCode
	}
}

func withMetrics(m *observability.Metrics) option {
	return func(d *Deps, _ *stubProvider) { d.Metrics = m }
}

func withRetryAfter(d time.Duration) option {
	return func(_ *Deps, p *stubProvider) { p.retryAfter = d }
}

func withUpstreamCode(code string) option {
	return func(_ *Deps, p *stubProvider) { p.upstreamCode = code }
}

func newTestServer(t *testing.T, opts ...option) *testServer {
	t.Helper()

	stub := &stubProvider{class: provider.ClassOK}
	ready := atomic.Bool{}
	ready.Store(true)

	deps := Deps{
		APIToken:     "secret-token",
		MaxBodyBytes: 1 << 16,
		Ready:        ready.Load,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(&deps, stub)
	}

	relayCfg := relay.Config{
		Provider: stub,
		Retries:  0,
		Timeout:  time.Second,
		Logger:   deps.Logger,
		Sleep:    func(context.Context, time.Duration) error { return nil },
	}
	if deps.Metrics != nil {
		relayCfg.Metrics = deps.Metrics
	}
	deps.Relay = relay.New(relayCfg)

	return &testServer{handler: NewRouter(deps), stub: stub}
}

func (s *testServer) do(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func authHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
}

func TestSendSuccess(t *testing.T) {
	s := newTestServer(t)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hello"}`, authHeader("secret-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{`"ok":true`, `"errcode":0`, `"errmsg":"ok"`, `"provider":"stub"`, `"request_id"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response %s does not contain %s", body, want)
		}
	}
	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Error("X-Request-Id header missing")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := s.stub.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
}

func TestAuthRejectsWithoutToken(t *testing.T) {
	s := newTestServer(t)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeUnauthorized) {
		t.Errorf("body %s does not contain unauthorized code", rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("WWW-Authenticate header missing")
	}
	if got := s.stub.calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}

	wrong := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("nope"))
	if wrong.Code != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", wrong.Code)
	}
}

func TestAuthSkipsProbes(t *testing.T) {
	s := newTestServer(t)

	if rec := s.do(t, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 without token", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("readyz status = %d, want 200 without token", rec.Code)
	}
}

func TestMetricsRequireAuth(t *testing.T) {
	metrics := observability.NewMetrics()
	s := newTestServer(t, withMetrics(metrics))

	if rec := s.do(t, http.MethodGet, "/metrics", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("metrics without token status = %d, want 401", rec.Code)
	}

	s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))

	rec := s.do(t, http.MethodGet, "/metrics", "", authHeader("secret-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	for _, want := range []string{
		"dingtalk_robot_deliveries_total",
		"dingtalk_robot_delivery_duration_seconds",
		"dingtalk_robot_requests_total",
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics output missing %s", want)
		}
	}
}

func TestReadinessReflectsState(t *testing.T) {
	s := newTestServer(t, func(d *Deps, _ *stubProvider) { d.Ready = func() bool { return false } })

	rec := s.do(t, http.MethodGet, "/readyz", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	// 存活探针不受就绪状态影响。
	if rec := s.do(t, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", rec.Code)
	}
}

func TestRootAndUnknownPathReturnJSON404(t *testing.T) {
	s := newTestServer(t)

	// 未认证时不会探测路由是否存在。
	if rec := s.do(t, http.MethodGet, "/", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET / status = %d, want 401", rec.Code)
	}

	for _, path := range []string{"/", "/nope/deep"} {
		rec := s.do(t, http.MethodGet, path, "", map[string]string{"Authorization": "Bearer secret-token"})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want application/json", path, ct)
		}
		if !strings.Contains(rec.Body.String(), apierr.CodeNotFound) {
			t.Errorf("GET %s body %s missing %s", path, rec.Body.String(), apierr.CodeNotFound)
		}
		if strings.Contains(rec.Body.String(), "DingTalk robot is running") {
			t.Error("root path still returns the legacy health text")
		}
	}
}

func TestLegacyPathIsGone(t *testing.T) {
	s := newTestServer(t)

	rec := s.do(t, http.MethodPost, "/robot/send", `{"text":"hi"}`, authHeader("secret-token"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for the removed path", rec.Code)
	}
	for _, header := range []string{"Deprecation", "Sunset", "Link"} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s = %q, want no compatibility header", header, got)
		}
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeNotFound) {
		t.Errorf("body %s missing %s", rec.Body.String(), apierr.CodeNotFound)
	}
	if got := s.stub.calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

func TestPayloadTooLarge(t *testing.T) {
	s := newTestServer(t, withMaxBody(32))

	big := `{"text":"` + strings.Repeat("a", 100) + `"}`
	rec := s.do(t, http.MethodPost, "/v1/messages", big, authHeader("secret-token"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), apierr.CodePayloadTooLarge) {
		t.Errorf("body %s missing payload_too_large", rec.Body.String())
	}
	if got := s.stub.calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

func TestUnsupportedMediaType(t *testing.T) {
	s := newTestServer(t)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, map[string]string{
		"Authorization": "Bearer secret-token",
		"Content-Type":  "text/plain",
	})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestInvalidJSONAndUnknownType(t *testing.T) {
	s := newTestServer(t)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":`, authHeader("secret-token"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), apierr.CodeInvalidJSON) {
		t.Errorf("broken json: status %d body %s", rec.Code, rec.Body.String())
	}

	rec = s.do(t, http.MethodPost, "/v1/messages", `{"msgtype":"image"}`, authHeader("secret-token"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), apierr.CodeUnsupportedMessageType) {
		t.Errorf("unknown type: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMessageValidationEnforcedBeforeDelivery(t *testing.T) {
	tooManyAt := `{"msgtype":"text","text":{"content":"hi"},"at":{"atMobiles":[` +
		strings.Repeat(`"13800000000",`, message.MaxAtMentions) + `"13800000000"]}}`

	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "blank text", body: `{"text":"   "}`, code: apierr.CodeInvalidMessage},
		{name: "markdown without title", body: `{"msgtype":"markdown","markdown":{"text":"# h"}}`, code: apierr.CodeInvalidMessage},
		{name: "link without messageUrl", body: `{"msgtype":"link","link":{"title":"t","text":"x"}}`, code: apierr.CodeInvalidMessage},
		{name: "actionCard without target", body: `{"msgtype":"actionCard","actionCard":{"title":"t","text":"x"}}`, code: apierr.CodeInvalidMessage},
		{name: "too many at mentions", body: tooManyAt, code: apierr.CodeAtLimitExceeded},
		{
			name: "text over the size limit",
			body: `{"text":"` + strings.Repeat("a", message.MaxTextBytes+1) + `"}`,
			code: apierr.CodeMessageTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)

			rec := s.do(t, http.MethodPost, "/v1/messages", tc.body, authHeader("secret-token"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.code) {
				t.Errorf("body %s does not contain %s", rec.Body.String(), tc.code)
			}
			if got := s.stub.calls.Load(); got != 0 {
				t.Errorf("provider calls = %d, want 0 for an invalid message", got)
			}
		})
	}
}

func TestErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		class    provider.ErrorClass
		errCode  int64
		status   int
		code     string
		retryHdr bool
	}{
		{name: "upstream rejected", class: provider.ClassRejected, errCode: 430101, status: 502, code: apierr.CodeUpstreamRejected},
		{name: "config error", class: provider.ClassConfig, errCode: 310000, status: 502, code: apierr.CodeUpstreamRejected},
		{name: "throttled", class: provider.ClassThrottled, errCode: 410100, status: 429, code: apierr.CodeRateLimited, retryHdr: true},
		{name: "transport failure", class: provider.ClassRetryable, errCode: 0, status: 504, code: apierr.CodeUpstreamTimeout},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, withResult(tc.class, tc.errCode))
			rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.code) {
				t.Errorf("body %s does not contain %s", body, tc.code)
			}
			if !strings.Contains(body, `"ok":false`) {
				t.Errorf("body %s should report ok=false", body)
			}
			if tc.retryHdr && rec.Header().Get("Retry-After") == "" {
				t.Error("Retry-After header missing")
			}
			if tc.errCode != 0 && !strings.Contains(body, `"errcode":`+strconv.FormatInt(tc.errCode, 10)) {
				t.Errorf("body %s does not carry upstream errcode %d", body, tc.errCode)
			}
		})
	}
}

func TestRateLimitedRetryAfterFollowsChannel(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter time.Duration
		want       string
	}{
		{name: "enterprise style short wait", retryAfter: time.Second, want: "1"},
		{name: "custom style ban window", retryAfter: 600 * time.Second, want: "600"},
		{name: "unset falls back to the ban window", retryAfter: 0, want: "600"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, withResult(provider.ClassThrottled, 410100), withRetryAfter(tc.retryAfter))

			rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d (body=%s), want 429", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Errorf("Retry-After = %q, want %q", got, tc.want)
			}
			if got := s.stub.calls.Load(); got != 1 {
				t.Errorf("provider calls = %d, want 1 (no retry on throttle)", got)
			}
		})
	}
}

func TestAtDroppedReported(t *testing.T) {
	s := newTestServer(t, func(_ *Deps, p *stubProvider) { p.atDropped = true })

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi","at":{"atMobiles":["13800000000"]}}`, authHeader("secret-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"at_dropped":true`) {
		t.Errorf("body %s should report at_dropped", rec.Body.String())
	}
}

func TestRequestIDIsEchoed(t *testing.T) {
	s := newTestServer(t)

	headers := authHeader("secret-token")
	headers["X-Request-Id"] = "trace-123"
	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, headers)

	if got := rec.Header().Get("X-Request-Id"); got != "trace-123" {
		t.Errorf("X-Request-Id = %q, want trace-123", got)
	}
	if !strings.Contains(rec.Body.String(), `"request_id":"trace-123"`) {
		t.Errorf("body %s does not echo request_id", rec.Body.String())
	}

	// 非法字符不会被回显。
	headers["X-Request-Id"] = "bad id\twith\ttabs"
	rec = s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, headers)
	if got := rec.Header().Get("X-Request-Id"); got == "bad id\twith\ttabs" {
		t.Error("unsafe request id was echoed back")
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	s := &server{deps: Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	handler := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeInternal) {
		t.Errorf("body %s missing internal code", rec.Body.String())
	}
}

func TestMethodNotAllowedReturnsJSON(t *testing.T) {
	s := newTestServer(t)
	rec := s.do(t, http.MethodGet, "/v1/messages", "", map[string]string{"Authorization": "Bearer secret-token"})

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on send route status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeMethodNotAllowed) {
		t.Errorf("body %s missing %s", rec.Body.String(), apierr.CodeMethodNotAllowed)
	}
	if got := s.stub.calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

func TestErrorResponseSeparatesUpstreamCodes(t *testing.T) {
	s := newTestServer(t,
		withResult(provider.ClassConfig, 0),
		withUpstreamCode("InvalidAuthentication"),
	)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["upstream_code"] != "InvalidAuthentication" {
		t.Errorf("upstream_code = %v, want InvalidAuthentication", body["upstream_code"])
	}
	if body["errcode"] != float64(0) {
		t.Errorf("errcode = %v, want 0 for a non-numeric upstream code", body["errcode"])
	}
	if body["code"] != apierr.CodeUpstreamRejected {
		t.Errorf("code = %v, want %s", body["code"], apierr.CodeUpstreamRejected)
	}
}

func TestThrottledResponseKeepsUpstreamCode(t *testing.T) {
	s := newTestServer(t,
		withResult(provider.ClassThrottled, 0),
		withUpstreamCode("Forbidden.AccessDenied.QpsLimitForAppkeyAndApi"),
		withRetryAfter(time.Second),
	)

	rec := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"upstream_code":"Forbidden.AccessDenied.QpsLimitForAppkeyAndApi"`) {
		t.Errorf("body %s missing the upstream business code", body)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
}

func TestWaitTimeoutRateLimitOmitsRetryAfter(t *testing.T) {
	stub := &stubProvider{class: provider.ClassOK}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	deps := Deps{
		APIToken:     "secret-token",
		MaxBodyBytes: 1 << 16,
		Ready:        func() bool { return true },
		Logger:       logger,
	}
	deps.Relay = relay.New(relay.Config{
		Provider:  stub,
		RateLimit: rate.NewLimiter(rate.Every(time.Hour), 1),
		Strategy:  config.StrategyWait,
		Timeout:   30 * time.Millisecond,
		Logger:    logger,
	})
	s := &testServer{handler: NewRouter(deps), stub: stub}

	first := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (body=%s)", first.Code, first.Body.String())
	}

	// 令牌桶在接下来一小时内不再发放令牌，排队必然超过 send.timeout。
	second := s.do(t, http.MethodPost, "/v1/messages", `{"text":"hi"}`, authHeader("secret-token"))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429 (body=%s)", second.Code, second.Body.String())
	}
	if got := second.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want no header when the layer has no wait advice", got)
	}
	if !strings.Contains(second.Body.String(), apierr.CodeRateLimited) {
		t.Errorf("body %s missing %s", second.Body.String(), apierr.CodeRateLimited)
	}
}

func TestAtOnLinkRejectedBeforeDelivery(t *testing.T) {
	s := newTestServer(t)

	body := `{"msgtype":"link","link":{"title":"t","text":"x","messageUrl":"https://example.com"},"at":{"atMobiles":["150XXXXXXXX"]}}`
	rec := s.do(t, http.MethodPost, "/v1/messages", body, authHeader("secret-token"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), apierr.CodeAtNotSupported) {
		t.Errorf("body %s missing %s", rec.Body.String(), apierr.CodeAtNotSupported)
	}
	if got := s.stub.calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0: at must be rejected before delivery", got)
	}
}

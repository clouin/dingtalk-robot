// Package e2e 验证从 HTTP 入口到上游的真实装配链路。
//
// 这里的用例刻意穿过 router、中间件链、relay 与真实 provider，只在最外层替换
// 上游服务器；单包测试各自用 stub 保持纯粹，链路完整性由本包负责。
package e2e

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dingtalk-robot/internal/httpapi"
	"dingtalk-robot/internal/observability"
	"dingtalk-robot/internal/provider/custom"
	"dingtalk-robot/internal/provider/enterprise"
	"dingtalk-robot/internal/relay"
)

const testToken = "entry-token"

// rewriteTransport 把发往钉钉官方地址的请求转到本地 httptest 服务器，
// 其余部分（查询参数、请求体、方法）保持原样，因此签名逻辑仍被真实执行。
type rewriteTransport struct {
	target *url.URL
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// newStack 组装 router → relay → custom provider → 上游 的完整链路。
func newStack(t *testing.T, upstream string) http.Handler {
	t.Helper()

	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics()

	prov := custom.New(custom.Config{
		AccessToken: "webhook-token",
		Secret:      "signing-secret",
		Client:      &http.Client{Transport: rewriteTransport{target: target}},
	})

	sender := relay.New(relay.Config{
		Provider:    prov,
		Strategy:    "wait",
		Retries:     2,
		BackoffBase: time.Millisecond,
		Timeout:     5 * time.Second,
		Metrics:     metrics,
		Logger:      logger,
	})

	return httpapi.NewRouter(httpapi.Deps{
		Relay:        sender,
		APIToken:     testToken,
		MaxBodyBytes: 65536,
		Ready:        func() bool { return true },
		Metrics:      metrics,
		Logger:       logger,
	})
}

// splitTransport 把企业通道发往官方地址的请求分流到两个测试服务器：
// accessToken 走 token，其余走 send。
type splitTransport struct {
	token *url.URL
	send  *url.URL
}

func (s splitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := s.send
	if strings.HasSuffix(req.URL.Path, "/oauth2/accessToken") {
		base = s.token
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = base.Scheme
	clone.URL.Host = base.Host
	clone.Host = base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// newEnterpriseStack 组装 router → relay → enterprise provider → 上游 的完整链路。
func newEnterpriseStack(t *testing.T, tokenUpstream, sendUpstream string) http.Handler {
	t.Helper()

	tokenTarget, err := url.Parse(tokenUpstream)
	if err != nil {
		t.Fatalf("parse token upstream url: %v", err)
	}
	sendTarget, err := url.Parse(sendUpstream)
	if err != nil {
		t.Fatalf("parse send upstream url: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics()

	prov := enterprise.New(enterprise.Config{
		ClientID:           "app-key",
		ClientSecret:       "app-secret",
		RobotCode:          "robot",
		OpenConversationID: "cid",
		Client:             &http.Client{Transport: splitTransport{token: tokenTarget, send: sendTarget}},
	})

	sender := relay.New(relay.Config{
		Provider: prov,
		Strategy: "wait",
		Retries:  0,
		Timeout:  5 * time.Second,
		Metrics:  metrics,
		Logger:   logger,
	})

	return httpapi.NewRouter(httpapi.Deps{
		Relay:        sender,
		APIToken:     testToken,
		MaxBodyBytes: 65536,
		Ready:        func() bool { return true },
		Metrics:      metrics,
		Logger:       logger,
	})
}

func postMessage(t *testing.T, handler http.Handler, body, requestID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	if requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// capturedBody 指向上游收到的报文，请求结束后由断言读取。
type capturedBody struct {
	url   string
	value *map[string]any
}

// captureUpstreamBody 起一个固定成功的上游，并把收到的报文解到返回值里。
func captureUpstreamBody(t *testing.T) capturedBody {
	t.Helper()

	body := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(server.Close)

	return capturedBody{url: server.URL, value: &body}
}

func TestDeliversThroughFullChain(t *testing.T) {
	var (
		gotQuery url.Values
		gotBody  map[string]any
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer upstream.Close()

	rec := postMessage(t, newStack(t, upstream.URL), `{"text":"hello"}`, "8ba25b9e5dfb4576")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
	if resp["request_id"] != "8ba25b9e5dfb4576" {
		t.Errorf("request_id = %v, want the caller supplied value", resp["request_id"])
	}
	if resp["provider"] != "custom" {
		t.Errorf("provider = %v, want custom", resp["provider"])
	}

	// 上游必须收到签名与原始载荷：入口到通道的参数传递没有被中间层吞掉。
	if got := gotQuery.Get("access_token"); got != "webhook-token" {
		t.Errorf("upstream access_token = %q, want webhook-token", got)
	}
	if gotQuery.Get("sign") == "" || gotQuery.Get("timestamp") == "" {
		t.Errorf("upstream query lacks signature parameters: %v", gotQuery)
	}
	if gotBody["msgtype"] != "text" {
		t.Errorf("upstream msgtype = %v, want text", gotBody["msgtype"])
	}
	text, _ := gotBody["text"].(map[string]any)
	if text["content"] != "hello" {
		t.Errorf("upstream text.content = %v, want hello", text["content"])
	}
}

func TestSimplifiedBodyWithAtReachesUpstream(t *testing.T) {
	gotBody := captureUpstreamBody(t)

	rec := postMessage(t, newStack(t, gotBody.url), `{"text":"hello","at":{"atMobiles":["150XXXXXXXX"]}}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	at, _ := (*gotBody.value)["at"].(map[string]any)
	mobiles, _ := at["atMobiles"].([]any)
	if len(mobiles) != 1 || mobiles[0] != "150XXXXXXXX" {
		t.Errorf("upstream at.atMobiles = %v, want the caller supplied mobile", at)
	}
}

func TestMarkdownMentionsReachUpstreamBody(t *testing.T) {
	gotBody := captureUpstreamBody(t)

	rec := postMessage(t, newStack(t, gotBody.url),
		`{"msgtype":"markdown","markdown":{"title":"t","text":"### done"},"at":{"atMobiles":["150XXXXXXXX"]}}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	markdown, _ := (*gotBody.value)["markdown"].(map[string]any)
	text, _ := markdown["text"].(string)
	if !strings.Contains(text, "@150XXXXXXXX") {
		t.Errorf("upstream markdown.text = %q, want it to mention the mobile", text)
	}
}

func TestUpstreamRejectionSurfacesAsBadGateway(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":310000,"errmsg":"sign not match"}`)
	}))
	defer upstream.Close()

	rec := postMessage(t, newStack(t, upstream.URL), `{"text":"hello"}`, "")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] != "upstream_rejected" {
		t.Errorf("code = %v, want upstream_rejected", resp["code"])
	}
	if errcode, _ := resp["errcode"].(float64); errcode != 310000 {
		t.Errorf("errcode = %v, want 310000 passed through from upstream", resp["errcode"])
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1: configuration errors must not be retried", got)
	}
}

func TestEnterpriseUpstreamCodeSurfacesThroughChain(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accessToken":"token-1","expireIn":7200}`)
	}))
	defer tokenServer.Close()

	sendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"Forbidden.AccessDenied.QpsLimitForAppkeyAndApi","message":"qps"}`)
	}))
	defer sendServer.Close()

	rec := postMessage(t, newEnterpriseStack(t, tokenServer.URL, sendServer.URL), `{"text":"hi"}`, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] != "rate_limited" {
		t.Errorf("code = %v, want rate_limited", resp["code"])
	}
	if resp["upstream_code"] != "Forbidden.AccessDenied.QpsLimitForAppkeyAndApi" {
		t.Errorf("upstream_code = %v, want the raw enterprise business code", resp["upstream_code"])
	}
	if resp["errcode"] != float64(0) {
		t.Errorf("errcode = %v, want 0 for a string business code", resp["errcode"])
	}
	if resp["provider"] != "enterprise" {
		t.Errorf("provider = %v, want enterprise", resp["provider"])
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
}

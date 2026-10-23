package custom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
)

const (
	testTimestamp = "1700000000000"
	testSecret    = "SECtest"
	// goldenSign 由官方算法独立算出：base64(HMAC-SHA256("SECtest", "1700000000000\nSECtest"))。
	goldenSign = "aZLLrriXgn05YbwaGR7knYsLeJADjr9NwLaNNKpxh4g="
)

func TestSignMatchesOfficialVector(t *testing.T) {
	if got := Sign(testTimestamp, testSecret); got != goldenSign {
		t.Errorf("Sign = %q, want %q", got, goldenSign)
	}
}

func TestSignedURLEncodesSignature(t *testing.T) {
	fixed := time.UnixMilli(1700000000000)

	raw, err := SignedURL("https://oapi.dingtalk.com/robot/send", "tok", testSecret, fixed)
	if err != nil {
		t.Fatalf("SignedURL returned error: %v", err)
	}

	if !strings.Contains(raw, "sign=aZLLrriXgn05YbwaGR7knYsLeJADjr9NwLaNNKpxh4g%3D") {
		t.Errorf("signature is not URL-escaped in %q", raw)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("access_token"); got != "tok" {
		t.Errorf("access_token = %q, want tok", got)
	}
	if got := query.Get("timestamp"); got != testTimestamp {
		t.Errorf("timestamp = %q, want %s", got, testTimestamp)
	}
	if got := query.Get("sign"); got != goldenSign {
		t.Errorf("decoded sign = %q, want %q", got, goldenSign)
	}
}

func TestSignedURLWithoutSecret(t *testing.T) {
	raw, err := SignedURL("https://oapi.dingtalk.com/robot/send", "tok", "", time.Now())
	if err != nil {
		t.Fatalf("SignedURL returned error: %v", err)
	}
	parsed, _ := url.Parse(raw)
	query := parsed.Query()
	if query.Get("sign") != "" || query.Get("timestamp") != "" {
		t.Errorf("unsigned URL must not carry sign/timestamp: %q", raw)
	}
	if query.Get("access_token") != "tok" {
		t.Errorf("access_token missing in %q", raw)
	}
}

func TestThrottledResultCarriesBanWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":410100,"errmsg":"send too fast"}`))
	}))
	defer server.Close()

	p := New(Config{AccessToken: "tok", Client: rewriteTo(t, server.URL)})
	result := p.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})

	if result.Class != provider.ClassThrottled {
		t.Fatalf("class = %q, want throttled", result.Class)
	}
	// 官方超限后封禁 10 分钟，等待建议必须与其对齐。
	if result.RetryAfter != provider.CustomThrottleRetryAfter {
		t.Errorf("RetryAfter = %s, want %s", result.RetryAfter, provider.CustomThrottleRetryAfter)
	}
}

func TestSendClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantClass   provider.ErrorClass
		wantErrCode int64
	}{
		{name: "success", status: 200, body: `{"errcode":0,"errmsg":"ok"}`, wantClass: provider.ClassOK},
		{name: "throttled", status: 200, body: `{"errcode":410100,"errmsg":"send too fast"}`, wantClass: provider.ClassThrottled, wantErrCode: 410100},
		{name: "legacy throttle code", status: 200, body: `{"errcode":130101,"errmsg":"fast"}`, wantClass: provider.ClassThrottled, wantErrCode: 130101},
		{name: "quota exceeded", status: 200, body: `{"errcode":45009,"errmsg":"api limit"}`, wantClass: provider.ClassThrottled, wantErrCode: 45009},
		{name: "system busy", status: 200, body: `{"errcode":-1,"errmsg":"busy"}`, wantClass: provider.ClassRetryable},
		{name: "sign not match", status: 200, body: `{"errcode":310000,"errmsg":"sign not match"}`, wantClass: provider.ClassConfig},
		{name: "errcode as string", status: 200, body: `{"errcode":"400101","errmsg":"token"}`, wantClass: provider.ClassConfig},
		{name: "content rejected", status: 200, body: `{"errcode":430101,"errmsg":"unsafe"}`, wantClass: provider.ClassRejected},
		{name: "gateway error", status: 503, body: `<html>bad gateway</html>`, wantClass: provider.ClassRetryable},
		{name: "client error", status: 400, body: `{"errcode":40035,"errmsg":"param"}`, wantClass: provider.ClassRejected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			p := New(Config{AccessToken: "tok", Secret: testSecret, Client: rewriteTo(t, server.URL)})
			result := p.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})

			if result.Class != tc.wantClass {
				t.Errorf("class = %q, want %q (errmsg=%q)", result.Class, tc.wantClass, result.ErrMsg)
			}
			if tc.wantErrCode != 0 && result.ErrCode != tc.wantErrCode {
				t.Errorf("errcode = %d, want %d", result.ErrCode, tc.wantErrCode)
			}
		})
	}
}

func TestSendHonoursContextCancellation(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(blocked)

	p := New(Config{AccessToken: "tok", Client: rewriteTo(t, server.URL)})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := p.Send(ctx, &message.Message{Type: message.TypeText, Text: "hi"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Send ignored cancellation, took %s", elapsed)
	}
	if result.Class != provider.ClassRetryable {
		t.Errorf("class = %q, want retryable", result.Class)
	}
}

func TestPayloadWireFormat(t *testing.T) {
	tests := []struct {
		name     string
		msg      message.Message
		wantType string
		contains string
	}{
		{name: "text", msg: message.Message{Type: message.TypeText, Text: "hi"}, wantType: "text", contains: `"content":"hi"`},
		{name: "markdown", msg: message.Message{Type: message.TypeMarkdown, Title: "t", Text: "# h"}, wantType: "markdown", contains: `"title":"t"`},
		{name: "link", msg: message.Message{Type: message.TypeLink, Link: message.Link{Title: "t", Text: "x", MessageURL: "https://u"}}, wantType: "link", contains: `"messageUrl":"https://u"`},
		{name: "actionCard", msg: message.Message{Type: message.TypeActionCard, Card: message.ActionCard{Title: "t", Text: "x", Buttons: []message.Button{{Title: "a", ActionURL: "https://a"}}}}, wantType: "actionCard", contains: `"actionURL":"https://a"`},
		{name: "feedCard", msg: message.Message{Type: message.TypeFeedCard, Feed: []message.FeedLink{{Title: "t", MessageURL: "https://u"}}}, wantType: "feedCard", contains: `"messageURL":"https://u"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := Payload(&tc.msg)
			if err != nil {
				t.Fatalf("Payload returned error: %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("payload is not valid JSON: %v", err)
			}
			if decoded["msgtype"] != tc.wantType {
				t.Errorf("msgtype = %v, want %s", decoded["msgtype"], tc.wantType)
			}
			if !strings.Contains(string(payload), tc.contains) {
				t.Errorf("payload %s does not contain %s", payload, tc.contains)
			}
		})
	}
}

func TestPayloadCarriesAt(t *testing.T) {
	payload, err := Payload(&message.Message{
		Type:  message.TypeMarkdown,
		Title: "t",
		Text:  "# h",
		At:    message.At{Mobiles: []string{"13800000000"}, UserIDs: []string{"u1"}},
	})
	if err != nil {
		t.Fatalf("Payload returned error: %v", err)
	}
	for _, want := range []string{`"atMobiles":["13800000000"]`, `"atUserIds":["u1"]`} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("payload %s does not contain %s", payload, want)
		}
	}
}

func TestName(t *testing.T) {
	if got := New(Config{AccessToken: "t"}).Name(); got != "custom" {
		t.Errorf("Name = %q, want custom", got)
	}
}

// rewriteTo 把请求重写到测试服务器，使 provider 无需暴露可配置地址。
func rewriteTo(t *testing.T, target string) *http.Client {
	t.Helper()
	base, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = base.Scheme
		req.URL.Host = base.Host
		return http.DefaultTransport.RoundTrip(req)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

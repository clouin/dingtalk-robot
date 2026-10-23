package enterprise

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
)

// fixture 是一组假钉钉端点。
type fixture struct {
	tokenServer *httptest.Server
	sendServer  *httptest.Server
	tokenCalls  atomic.Int32
	sendCalls   atomic.Int32
	lastSend    atomic.Value // sendRequest
	lastToken   atomic.Value // string

	sendHandler  func(w http.ResponseWriter, r *http.Request)
	tokenHandler func(w http.ResponseWriter, r *http.Request)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}

	f.tokenServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		var req tokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastToken.Store(req.AppKey)
		if f.tokenHandler != nil {
			f.tokenHandler(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "token-1", ExpireIn: 7200})
	}))

	f.sendServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sendCalls.Add(1)
		var req sendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastSend.Store(req)
		if f.sendHandler != nil {
			f.sendHandler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"processQueryKey":"q"}`))
	}))

	t.Cleanup(func() {
		f.tokenServer.Close()
		f.sendServer.Close()
	})
	return f
}

func (f *fixture) provider(t *testing.T, opts ...func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		ClientID:           "app-key",
		ClientSecret:       "app-secret",
		RobotCode:          "robot",
		OpenConversationID: "cid",
		Client:             f.client(t),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return New(cfg)
}

// client 把令牌请求与发送请求分别重写到两个测试服务器，
// 这样 provider 不必对外暴露可配置的地址。
func (f *fixture) client(t *testing.T) *http.Client {
	t.Helper()

	tokenBase, err := url.Parse(f.tokenServer.URL)
	if err != nil {
		t.Fatalf("parse token server url: %v", err)
	}
	sendBase, err := url.Parse(f.sendServer.URL)
	if err != nil {
		t.Fatalf("parse send server url: %v", err)
	}

	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		base := sendBase
		if strings.HasSuffix(req.URL.Path, "/oauth2/accessToken") {
			base = tokenBase
		}
		req.URL.Scheme = base.Scheme
		req.URL.Host = base.Host
		return http.DefaultTransport.RoundTrip(req)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func textMessage() *message.Message {
	return &message.Message{Type: message.TypeText, Text: "hi"}
}

func TestTokenIsCached(t *testing.T) {
	f := newFixture(t)
	p := f.provider(t)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if result := p.Send(ctx, textMessage()); result.Class != provider.ClassOK {
			t.Fatalf("send %d failed: %+v", i, result)
		}
	}

	if got := f.tokenCalls.Load(); got != 1 {
		t.Errorf("token endpoint called %d times, want 1", got)
	}
	if got := f.sendCalls.Load(); got != 3 {
		t.Errorf("send endpoint called %d times, want 3", got)
	}
	if got := f.lastToken.Load(); got != "app-key" {
		t.Errorf("token request appKey = %v, want app-key", got)
	}
}

func TestTokenRefreshesBeforeExpiry(t *testing.T) {
	f := newFixture(t)

	now := time.Unix(1700000000, 0)
	p := f.provider(t, func(c *Config) { c.Now = func() time.Time { return now } })

	ctx := context.Background()
	if result := p.Send(ctx, textMessage()); result.Class != provider.ClassOK {
		t.Fatalf("first send failed: %+v", result)
	}

	now = now.Add(7200*time.Second - 301*time.Second)
	if result := p.Send(ctx, textMessage()); result.Class != provider.ClassOK {
		t.Fatalf("second send failed: %+v", result)
	}
	if got := f.tokenCalls.Load(); got != 1 {
		t.Errorf("token refreshed too early: %d calls", got)
	}

	now = now.Add(2 * time.Second) // 距过期 299 秒，进入提前刷新窗口
	if result := p.Send(ctx, textMessage()); result.Class != provider.ClassOK {
		t.Fatalf("third send failed: %+v", result)
	}
	if got := f.tokenCalls.Load(); got != 2 {
		t.Errorf("token endpoint called %d times, want 2 after entering refresh window", got)
	}
}

func TestConcurrentSendsShareOneTokenFetch(t *testing.T) {
	f := newFixture(t)
	f.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "token-1", ExpireIn: 7200})
	}
	p := f.provider(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := p.Send(context.Background(), textMessage()); result.Class != provider.ClassOK {
				t.Errorf("unexpected result: %+v", result)
			}
		}()
	}
	wg.Wait()

	if got := f.tokenCalls.Load(); got != 1 {
		t.Errorf("token endpoint called %d times, want exactly 1", got)
	}
}

func TestAuthFailureInvalidatesTokenAndRetriesOnce(t *testing.T) {
	f := newFixture(t)

	var tokenIssue atomic.Int32
	f.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		token := "token-1"
		if tokenIssue.Add(1) > 1 {
			token = "token-2"
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: token, ExpireIn: 7200})
	}

	f.sendHandler = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-acs-dingtalk-access-token") == "token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"InvalidAuthentication","message":"expired"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"processQueryKey":"q"}`))
	}

	p := f.provider(t)
	result := p.Send(context.Background(), textMessage())
	if result.Class != provider.ClassOK {
		t.Fatalf("class = %q (%s), want ok", result.Class, result.ErrMsg)
	}
	if got := f.sendCalls.Load(); got != 2 {
		t.Errorf("send calls = %d, want 2", got)
	}
	if got := f.tokenCalls.Load(); got != 2 {
		t.Errorf("token calls = %d, want 2", got)
	}
}

func TestAuthFailureEventuallyReported(t *testing.T) {
	f := newFixture(t)
	f.sendHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"InvalidAuthentication","message":"nope"}`))
	}

	p := f.provider(t)
	result := p.Send(context.Background(), textMessage())
	if result.Class != provider.ClassConfig {
		t.Errorf("class = %q, want config", result.Class)
	}
	if got := f.sendCalls.Load(); got != 2 {
		t.Errorf("send calls = %d, want exactly 2 (original + one retry)", got)
	}
}

func TestClassificationOfUpstreamFailures(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		body        string
		class       provider.ErrorClass
		wantRetryAt time.Duration
	}{
		{name: "throttled", code: 429, body: `{"code":"Throttling.Api","message":"slow down"}`, class: provider.ClassThrottled, wantRetryAt: time.Second},
		{name: "qps limit code", code: 403, body: `{"code":"Forbidden.AccessDenied.QpsLimitForAppkeyAndApi","message":"qps"}`, class: provider.ClassThrottled, wantRetryAt: time.Second},
		{name: "legacy throttle code in code", code: 403, body: `{"code":"90006","message":"too many calls"}`, class: provider.ClassThrottled, wantRetryAt: time.Second},
		{name: "legacy throttle code in message", code: 403, body: `{"code":"Forbidden.AccessDenied","message":"90018 too many calls"}`, class: provider.ClassThrottled, wantRetryAt: time.Second},
		// 鉴权类在 Send 层已经作废令牌重试过一次，对外统一降级为配置问题。
		{name: "forbidden without qps", code: 403, body: `{"code":"Forbidden.AccessDenied","message":"no permission"}`, class: provider.ClassConfig},
		{name: "invalid authentication", code: 401, body: `{"code":"InvalidAuthentication","message":"nope"}`, class: provider.ClassConfig},
		{name: "server error", code: 500, body: `{"code":"InternalError"}`, class: provider.ClassRetryable},
		{name: "bad request", code: 400, body: `{"code":"InvalidParameter","message":"bad"}`, class: provider.ClassRejected},
		{name: "unparseable", code: 502, body: `not json`, class: provider.ClassRetryable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.sendHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}
			p := f.provider(t)
			result := p.Send(context.Background(), textMessage())
			if result.Class != tc.class {
				t.Errorf("class = %q, want %q (%s)", result.Class, tc.class, result.ErrMsg)
			}
			if result.RetryAfter != tc.wantRetryAt {
				t.Errorf("RetryAfter = %s, want %s", result.RetryAfter, tc.wantRetryAt)
			}
		})
	}
}

func TestClassifyDistinguishesThrottleFromAuth(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		class  provider.ErrorClass
	}{
		{name: "qps limit", status: 403, code: "Forbidden.AccessDenied.QpsLimitForAppkeyAndApi", class: provider.ClassThrottled},
		{name: "legacy global limit", status: 200, code: "90002 global", class: provider.ClassThrottled},
		{name: "plain rate limit", status: 200, code: "TooManyRequests", class: provider.ClassThrottled},
		{name: "forbidden without limit hints", status: 403, code: "Forbidden.AccessDenied", class: provider.ClassAuth},
		{name: "invalid authentication", status: 401, code: "InvalidAuthentication", class: provider.ClassAuth},
		{name: "bad request", status: 400, code: "InvalidParameter", class: provider.ClassRejected},
		{name: "server error", status: 500, code: "InternalError", class: provider.ClassRetryable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.status, tc.code); got != tc.class {
				t.Errorf("classify(%d, %q) = %q, want %q", tc.status, tc.code, got, tc.class)
			}
		})
	}
}

func TestMessageTemplateMapping(t *testing.T) {
	tests := []struct {
		name       string
		msg        message.Message
		wantKey    string
		wantParams []string
	}{
		{
			name:       "text",
			msg:        message.Message{Type: message.TypeText, Text: "hi"},
			wantKey:    "sampleText",
			wantParams: []string{`"content":"hi"`},
		},
		{
			name:       "markdown",
			msg:        message.Message{Type: message.TypeMarkdown, Title: "t", Text: "# h"},
			wantKey:    "sampleMarkdown",
			wantParams: []string{`"title":"t"`, `"text":"# h"`},
		},
		{
			name:       "link",
			msg:        message.Message{Type: message.TypeLink, Link: message.Link{Title: "t", Text: "x", PicURL: "p", MessageURL: "u"}},
			wantKey:    "sampleLink",
			wantParams: []string{`"messageUrl":"u"`, `"picUrl":"p"`},
		},
		{
			name:       "actionCard single jump",
			msg:        message.Message{Type: message.TypeActionCard, Card: message.ActionCard{Title: "t", Text: "x", SingleTitle: "open", SingleURL: "https://a"}},
			wantKey:    "sampleActionCard",
			wantParams: []string{`"singleTitle":"open"`},
		},
		{
			name:       "actionCard one button",
			msg:        message.Message{Type: message.TypeActionCard, Card: message.ActionCard{Title: "t", Text: "x", Buttons: []message.Button{{Title: "a", ActionURL: "https://a"}}}},
			wantKey:    "sampleActionCard",
			wantParams: []string{`"singleTitle":"a"`, `"singleURL":"https://a"`},
		},
		{
			name:       "actionCard three buttons",
			msg:        message.Message{Type: message.TypeActionCard, Card: message.ActionCard{Title: "t", Text: "x", Buttons: threeButtons()}},
			wantKey:    "sampleActionCard3",
			wantParams: []string{`"actionTitle1":"b1"`, `"actionURL3":"https://b3"`},
		},
		{
			name:       "actionCard two horizontal buttons",
			msg:        message.Message{Type: message.TypeActionCard, Card: message.ActionCard{Title: "t", Text: "x", BtnOrientation: "1", Buttons: threeButtons()[:2]}},
			wantKey:    "sampleActionCard6",
			wantParams: []string{`"buttonTitle1":"b1"`, `"buttonUrl2":"https://b2"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.provider(t)

			msg := tc.msg
			if result := p.Send(context.Background(), &msg); result.Class != provider.ClassOK {
				t.Fatalf("send failed: %+v", result)
			}

			sent, ok := f.lastSend.Load().(sendRequest)
			if !ok {
				t.Fatal("no request captured")
			}
			if sent.MsgKey != tc.wantKey {
				t.Errorf("msgKey = %q, want %q", sent.MsgKey, tc.wantKey)
			}
			if sent.RobotCode != "robot" || sent.OpenConversationID != "cid" {
				t.Errorf("routing fields wrong: %+v", sent)
			}
			for _, want := range tc.wantParams {
				if !strings.Contains(sent.MsgParam, want) {
					t.Errorf("msgParam %s does not contain %s", sent.MsgParam, want)
				}
			}
		})
	}
}

func TestValidateRejectsUnsupportedCapabilities(t *testing.T) {
	f := newFixture(t)

	tests := []struct {
		name     string
		msg      message.Message
		opt      func(*Config)
		wantCode string
	}{
		{
			name:     "feedCard unsupported",
			msg:      message.Message{Type: message.TypeFeedCard, Feed: []message.FeedLink{{Title: "t"}}},
			wantCode: apierr.CodeUnsupportedMessageTypeForProvider,
		},
		{
			name:     "at unsupported",
			msg:      message.Message{Type: message.TypeText, Text: "hi", At: message.At{Mobiles: []string{"13800000000"}}},
			wantCode: apierr.CodeAtNotSupported,
		},
		{
			name: "six buttons",
			msg: message.Message{Type: message.TypeActionCard, Card: message.ActionCard{
				Title: "t", Text: "x",
				Buttons: append(threeButtons(),
					message.Button{Title: "b4", ActionURL: "https://b4"},
					message.Button{Title: "b5", ActionURL: "https://b5"},
					message.Button{Title: "b6", ActionURL: "https://b6"},
				),
			}},
			wantCode: apierr.CodeOptionNotSupported,
		},
		{
			name: "hideAvatar unsupported",
			msg: message.Message{Type: message.TypeActionCard, Card: message.ActionCard{
				Title: "t", Text: "x", SingleTitle: "a", SingleURL: "https://a", HideAvatar: "1",
			}},
			wantCode: apierr.CodeOptionNotSupported,
		},
		{
			name: "horizontal with three buttons unsupported",
			msg: message.Message{Type: message.TypeActionCard, Card: message.ActionCard{
				Title: "t", Text: "x", Buttons: threeButtons(), BtnOrientation: "1",
			}},
			wantCode: apierr.CodeOptionNotSupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := f.provider(t, func(c *Config) {
				if tc.opt != nil {
					tc.opt(c)
				}
			})
			msg := tc.msg
			err := p.Validate(&msg)
			apiErr, ok := apierr.As(err)
			if !ok {
				t.Fatalf("Validate returned %v, want *apierr.Error", err)
			}
			if apiErr.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", apiErr.Code, tc.wantCode)
			}
			if apiErr.Status != 400 {
				t.Errorf("status = %d, want 400", apiErr.Status)
			}
		})
	}
}

func TestIgnoreAtDropsMentions(t *testing.T) {
	f := newFixture(t)
	p := f.provider(t, func(c *Config) { c.IgnoreAt = true })

	msg := &message.Message{
		Type: message.TypeText,
		Text: "hi",
		At:   message.At{Mobiles: []string{"13800000000"}},
	}
	if err := p.Validate(msg); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	result := p.Send(context.Background(), msg)
	if result.Class != provider.ClassOK {
		t.Fatalf("send failed: %+v", result)
	}
	if !result.AtDropped {
		t.Error("AtDropped should be true when ignore_at drops mentions")
	}
}

func TestName(t *testing.T) {
	if got := New(Config{}).Name(); got != "enterprise" {
		t.Errorf("Name = %q, want enterprise", got)
	}
}

func TestUpstreamCodeIsReported(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantClass   provider.ErrorClass
		wantCode    string
		wantErrCode int64
	}{
		{
			name:        "string business code",
			status:      http.StatusBadRequest,
			body:        `{"code":"InvalidParameter","message":"bad"}`,
			wantClass:   provider.ClassRejected,
			wantCode:    "InvalidParameter",
			wantErrCode: 0,
		},
		{
			name:        "numeric business code",
			status:      http.StatusForbidden,
			body:        `{"code":"90006","message":"too many calls"}`,
			wantClass:   provider.ClassThrottled,
			wantCode:    "90006",
			wantErrCode: 90006,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.sendHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}

			result := f.provider(t).Send(context.Background(), textMessage())
			if result.Class != tc.wantClass {
				t.Fatalf("class = %q, want %q (%s)", result.Class, tc.wantClass, result.ErrMsg)
			}
			if result.UpstreamCode != tc.wantCode {
				t.Errorf("UpstreamCode = %q, want %q", result.UpstreamCode, tc.wantCode)
			}
			if result.ErrCode != tc.wantErrCode {
				t.Errorf("ErrCode = %d, want %d", result.ErrCode, tc.wantErrCode)
			}
		})
	}
}

func TestIgnoreAtSelectsRejectionOrDrop(t *testing.T) {
	msg := &message.Message{
		Type: message.TypeText,
		Text: "hi",
		At:   message.At{Mobiles: []string{"13800000000"}},
	}

	rejecting := newFixture(t)
	err := rejecting.provider(t).Validate(msg)
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Validate returned %v, want *apierr.Error", err)
	}
	if apiErr.Code != apierr.CodeAtNotSupported {
		t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeAtNotSupported)
	}
	if got := rejecting.sendCalls.Load(); got != 0 {
		t.Errorf("send calls = %d, want 0: rejected messages must not reach upstream", got)
	}

	// 响应层的 at_dropped 由 httpapi 的用例断言，这里固定通道侧的结果。
	dropping := newFixture(t)
	result := dropping.provider(t, func(c *Config) { c.IgnoreAt = true }).Send(context.Background(), msg)
	if result.Class != provider.ClassOK {
		t.Fatalf("send failed: %+v", result)
	}
	if !result.AtDropped {
		t.Error("AtDropped should be true when ignore_at drops mentions")
	}
}

func threeButtons() []message.Button {
	return []message.Button{
		{Title: "b1", ActionURL: "https://b1"},
		{Title: "b2", ActionURL: "https://b2"},
		{Title: "b3", ActionURL: "https://b3"},
	}
}

package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/config"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
	"dingtalk-robot/internal/provider/custom"
)

// scriptedProvider 按脚本顺序返回分类，并记录调用次数。
type scriptedProvider struct {
	mu          sync.Mutex
	classes     []provider.ErrorClass
	errCode     int64
	retryAfter  time.Duration
	validateErr error
	calls       int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Validate(*message.Message) error { return p.validateErr }

func (p *scriptedProvider) Send(context.Context, *message.Message) provider.Result {
	p.mu.Lock()
	defer p.mu.Unlock()

	class := provider.ClassOK
	if p.calls < len(p.classes) {
		class = p.classes[p.calls]
	}
	p.calls++

	return provider.Result{
		Class:      class,
		ErrMsg:     "upstream message",
		ErrCode:    p.errCode,
		RetryAfter: p.retryAfter,
	}
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type recordingMetrics struct {
	mu         sync.Mutex
	deliveries []string
	rateLimits []string
}

func (m *recordingMetrics) ObserveDelivery(providerName, class string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries = append(m.deliveries, providerName+"/"+class)
}

func (m *recordingMetrics) IncRateLimit(action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimits = append(m.rateLimits, action)
}

func newRelay(t *testing.T, p provider.Provider, opts ...func(*Config)) (*Relay, *recordingMetrics) {
	t.Helper()
	metrics := &recordingMetrics{}
	cfg := Config{
		Provider:    p,
		Retries:     2,
		BackoffBase: 1 * time.Millisecond,
		Timeout:     2 * time.Second,
		Metrics:     metrics,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return New(cfg), metrics
}

func TestSendRetriesRetryableThenSucceeds(t *testing.T) {
	p := &scriptedProvider{classes: []provider.ErrorClass{
		provider.ClassRetryable,
		provider.ClassOK,
	}}
	r, metrics := newRelay(t, p)

	outcome, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if outcome.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", outcome.Attempts)
	}
	if got := p.callCount(); got != 2 {
		t.Errorf("provider calls = %d, want 2", got)
	}
	if len(metrics.deliveries) != 2 {
		t.Errorf("deliveries recorded = %d, want 2", len(metrics.deliveries))
	}
}

func TestSendDoesNotRetryThrottled(t *testing.T) {
	p := &scriptedProvider{classes: []provider.ErrorClass{provider.ClassThrottled}, errCode: 410100}
	r, _ := newRelay(t, p, func(c *Config) { c.Retries = 5 })

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", err)
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (no retry on throttle)", got)
	}
	if apiErr.Code != apierr.CodeRateLimited || apiErr.Status != 429 {
		t.Errorf("got %d/%s, want 429/%s", apiErr.Status, apiErr.Code, apierr.CodeRateLimited)
	}
	// 钉钉超限封禁 10 分钟，建议等待时长必须与其对齐。
	if apiErr.RetryAfter != 600*time.Second {
		t.Errorf("RetryAfter = %s, want 600s", apiErr.RetryAfter)
	}
	if apiErr.RetryAfter != UpstreamThrottleRetryAfter {
		t.Errorf("RetryAfter = %s, want %s", apiErr.RetryAfter, UpstreamThrottleRetryAfter)
	}
}

func TestThrottledRetryAfterFollowsChannel(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter time.Duration
		want       time.Duration
	}{
		{name: "channel suggestion wins", retryAfter: time.Second, want: time.Second},
		{name: "zero falls back to the ban window", retryAfter: 0, want: UpstreamThrottleRetryAfter},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedProvider{classes: []provider.ErrorClass{provider.ClassThrottled}, retryAfter: tc.retryAfter}
			r, _ := newRelay(t, p)

			_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
			apiErr, ok := apierr.As(err)
			if !ok {
				t.Fatalf("Send returned %v, want *apierr.Error", err)
			}
			if apiErr.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %s, want %s", apiErr.RetryAfter, tc.want)
			}
		})
	}
}

func TestSendDoesNotRetryQuotaErrors(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":45009,"errmsg":"api limit"}`))
	}))
	defer upstream.Close()

	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	p := custom.New(custom.Config{
		AccessToken: "tok",
		Client:      &http.Client{Transport: rewriteTransport{target: base}},
	})
	r, _ := newRelay(t, p, func(c *Config) { c.Retries = 5 })

	_, sendErr := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(sendErr)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", sendErr)
	}
	if apiErr.Status != 429 || apiErr.Code != apierr.CodeRateLimited {
		t.Errorf("got %d/%s, want 429/%s", apiErr.Status, apiErr.Code, apierr.CodeRateLimited)
	}
	if apiErr.Upstream != 45009 {
		t.Errorf("upstream errcode = %d, want 45009", apiErr.Upstream)
	}
	if apiErr.RetryAfter != UpstreamThrottleRetryAfter {
		t.Errorf("RetryAfter = %s, want %s", apiErr.RetryAfter, UpstreamThrottleRetryAfter)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry on quota errors)", got)
	}
}

// rewriteTransport 把请求重写到 httptest 服务器，使 provider 无需暴露可配置地址。
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestSendDoesNotRetryConfigErrors(t *testing.T) {
	p := &scriptedProvider{classes: []provider.ErrorClass{provider.ClassConfig}, errCode: 310000}
	r, _ := newRelay(t, p)

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", err)
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
	if apiErr.Status != 502 || apiErr.Upstream != 310000 {
		t.Errorf("got %d/%d, want 502/310000", apiErr.Status, apiErr.Upstream)
	}
}

func TestSendExhaustsRetries(t *testing.T) {
	p := &scriptedProvider{
		classes: []provider.ErrorClass{provider.ClassRetryable, provider.ClassRetryable, provider.ClassRetryable},
		errCode: 500,
	}
	r, _ := newRelay(t, p)

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	if err == nil {
		t.Fatal("Send returned nil error, want failure")
	}
	if got := p.callCount(); got != 3 {
		t.Errorf("provider calls = %d, want 3 (1 + 2 retries)", got)
	}
}

func TestSendMapsTransportFailureToTimeout(t *testing.T) {
	p := &scriptedProvider{classes: []provider.ErrorClass{provider.ClassRetryable, provider.ClassRetryable, provider.ClassRetryable}}
	r, _ := newRelay(t, p)

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 504 || apiErr.Code != apierr.CodeUpstreamTimeout {
		t.Errorf("got %d/%s, want 504/%s", apiErr.Status, apiErr.Code, apierr.CodeUpstreamTimeout)
	}
}

func TestSendBackoffGrowsExponentially(t *testing.T) {
	p := &scriptedProvider{classes: []provider.ErrorClass{
		provider.ClassRetryable, provider.ClassRetryable, provider.ClassOK,
	}}

	var slept []time.Duration
	r, _ := newRelay(t, p, func(c *Config) {
		c.BackoffBase = 100 * time.Millisecond
		c.Sleep = func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}
	})

	if _, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("backoff[%d] = %s, want %s", i, slept[i], want[i])
		}
	}
}

func TestSendRejectsOnValidateError(t *testing.T) {
	p := &scriptedProvider{validateErr: apierr.New(400, apierr.CodeAtNotSupported, "no at")}
	r, _ := newRelay(t, p)

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 400 {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if got := p.callCount(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

func TestSendValidatesMessageBeforeProvider(t *testing.T) {
	p := &scriptedProvider{}
	r, _ := newRelay(t, p)

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "   "})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != apierr.CodeInvalidMessage {
		t.Errorf("got %d/%s, want 400/%s", apiErr.Status, apiErr.Code, apierr.CodeInvalidMessage)
	}
	if got := p.callCount(); got != 0 {
		t.Errorf("provider calls = %d, want 0 for an invalid message", got)
	}
}

func TestRateLimitRejectStrategy(t *testing.T) {
	p := &scriptedProvider{}
	limiter := rate.NewLimiter(rate.Limit(0), 1) // 只有 burst 一个额度
	r, metrics := newRelay(t, p, func(c *Config) {
		c.RateLimit = limiter
		c.Strategy = config.StrategyReject
	})

	if _, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"}); err != nil {
		t.Fatalf("first send should pass: %v", err)
	}

	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("second send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 429 {
		t.Errorf("status = %d, want 429", apiErr.Status)
	}
	if apiErr.RetryAfter <= 0 {
		t.Error("RetryAfter should be positive for the reject strategy")
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
	if len(metrics.rateLimits) != 1 || metrics.rateLimits[0] != "rejected" {
		t.Errorf("rate limit events = %v, want [rejected]", metrics.rateLimits)
	}
}

func TestRateLimitWaitRespectsContext(t *testing.T) {
	p := &scriptedProvider{}
	limiter := rate.NewLimiter(rate.Limit(0), 1)
	r, metrics := newRelay(t, p, func(c *Config) {
		c.RateLimit = limiter
		c.Strategy = config.StrategyWait
	})

	if _, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"}); err != nil {
		t.Fatalf("first send should pass: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := r.Send(ctx, &message.Message{Type: message.TypeText, Text: "hi"})
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("second send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 429 {
		t.Errorf("status = %d, want 429", apiErr.Status)
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
	if len(metrics.rateLimits) != 1 || metrics.rateLimits[0] != "cancelled" {
		t.Errorf("rate limit events = %v, want [cancelled]", metrics.rateLimits)
	}
}

func TestRateLimitWaitSharesSendBudget(t *testing.T) {
	p := &scriptedProvider{}
	limiter := rate.NewLimiter(rate.Limit(0), 1)
	r, metrics := newRelay(t, p, func(c *Config) {
		c.RateLimit = limiter
		c.Strategy = config.StrategyWait
		c.Timeout = 100 * time.Millisecond
	})

	if _, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"}); err != nil {
		t.Fatalf("first send should pass: %v", err)
	}

	start := time.Now()
	_, err := r.Send(context.Background(), &message.Message{Type: message.TypeText, Text: "hi"})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("queueing was not bounded by send.timeout: took %s", elapsed)
	}

	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("second send returned %v, want *apierr.Error", err)
	}
	if apiErr.Status != 429 {
		t.Errorf("status = %d, want 429", apiErr.Status)
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
	if len(metrics.rateLimits) != 1 || metrics.rateLimits[0] != "cancelled" {
		t.Errorf("rate limit events = %v, want [cancelled]", metrics.rateLimits)
	}
}

func TestProviderName(t *testing.T) {
	r, _ := newRelay(t, &scriptedProvider{})
	if got := r.ProviderName(); got != "scripted" {
		t.Errorf("ProviderName = %q, want scripted", got)
	}
}

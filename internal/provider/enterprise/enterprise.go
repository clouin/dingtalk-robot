// Package enterprise 实现钉钉企业内部应用机器人通道（v1.0 接口）。
package enterprise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
)

// SendEndpoint 是企业机器人发送群消息的接口地址。
const SendEndpoint = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"

// tokenEndpoint 是企业应用获取访问令牌的官方地址。
const tokenEndpoint = "https://api.dingtalk.com/v1.0/oauth2/accessToken"

// maxActionCardButtons 是模板能承载的最大按钮数。
const maxActionCardButtons = 5

// throttleRetryAfter 是企业通道被判限流后的建议等待时长。
//
// 该通道的限流是服务端 API 的 QPS 级约束（40 次/秒、1500 次/分等），等一个令牌
// 周期就能重试；沿用自定义机器人的 10 分钟会让调用方为一个秒级抖动退避过久。
const throttleRetryAfter = time.Second

const maxResponseBytes = 1 << 20

// Config 是通道配置。
type Config struct {
	ClientID           string
	ClientSecret       string
	RobotCode          string
	OpenConversationID string
	IgnoreAt           bool
	Client             *http.Client
	Now                func() time.Time
}

// Provider 是企业机器人通道。
type Provider struct {
	cfg    Config
	tokens *tokenCache
}

// New 创建通道。
func New(cfg Config) *Provider {
	if cfg.Client == nil {
		cfg.Client = provider.NewHTTPClient(0)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{cfg: cfg, tokens: &tokenCache{}}
}

// Name 返回通道名。
func (p *Provider) Name() string { return "enterprise" }

// Validate 检查企业通道能力：不支持 feedCard、@，以及超过 5 个按钮的卡片。
func (p *Provider) Validate(m *message.Message) error {
	if m.Type == message.TypeFeedCard {
		return apierr.New(http.StatusBadRequest, apierr.CodeUnsupportedMessageTypeForProvider,
			"feedCard is not supported by the enterprise robot API; use text, markdown, link or actionCard")
	}
	if m.HasAt() && !p.cfg.IgnoreAt {
		return apierr.New(http.StatusBadRequest, apierr.CodeAtNotSupported,
			"the enterprise robot API does not support @ mentions; enable dingtalk.enterprise.ignore_at to drop them")
	}
	if m.Type == message.TypeActionCard && len(m.Card.Buttons) > maxActionCardButtons {
		return apierr.New(http.StatusBadRequest, apierr.CodeOptionNotSupported,
			fmt.Sprintf("actionCard supports at most %d buttons on the enterprise robot API, got %d",
				maxActionCardButtons, len(m.Card.Buttons)))
	}
	if m.Type == message.TypeActionCard {
		if m.Card.HideAvatar == "1" {
			return apierr.New(http.StatusBadRequest, apierr.CodeOptionNotSupported,
				"hideAvatar is not supported by the enterprise robot API")
		}
		if m.Card.BtnOrientation == "1" && len(m.Card.Buttons) > 2 {
			return apierr.New(http.StatusBadRequest, apierr.CodeOptionNotSupported,
				fmt.Sprintf("horizontal buttons are only supported for exactly 2 buttons, got %d",
					len(m.Card.Buttons)))
		}
	}
	return nil
}

// Send 投递消息；鉴权失败时失效缓存令牌并重试一次。
func (p *Provider) Send(ctx context.Context, m *message.Message) provider.Result {
	result := p.deliver(ctx, m)
	if result.Class == provider.ClassAuth {
		p.tokens.invalidate()
		result = p.deliver(ctx, m)
	}
	if result.Class == provider.ClassAuth {
		result.Class = provider.ClassConfig
	}
	if m.HasAt() && p.cfg.IgnoreAt {
		result.AtDropped = true
	}
	return result
}

func (p *Provider) deliver(ctx context.Context, m *message.Message) provider.Result {
	msgKey, msgParam, err := buildMessage(m)
	if err != nil {
		return provider.Result{Class: provider.ClassRejected, ErrMsg: err.Error()}
	}

	token, err := p.tokens.get(ctx, p.cfg.Now(), p.fetchToken)
	if err != nil {
		return provider.Result{Class: provider.ClassRetryable, ErrMsg: err.Error()}
	}

	payload, err := json.Marshal(sendRequest{
		MsgParam:           string(msgParam),
		MsgKey:             msgKey,
		OpenConversationID: p.cfg.OpenConversationID,
		RobotCode:          p.cfg.RobotCode,
	})
	if err != nil {
		return provider.Result{Class: provider.ClassConfig, ErrMsg: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SendEndpoint, bytes.NewReader(payload))
	if err != nil {
		return provider.Result{Class: provider.ClassConfig, ErrMsg: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return provider.Result{Class: provider.ClassRetryable, ErrMsg: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return provider.Result{Class: provider.ClassRetryable, ErrMsg: err.Error()}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return provider.Result{Class: provider.ClassOK, ErrMsg: "ok"}
	}

	var failure errorResponse
	_ = json.Unmarshal(body, &failure)

	errMsg := failure.Message
	if errMsg == "" {
		errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if failure.Code != "" {
		errMsg = failure.Code + ": " + errMsg
	}

	// 业务码是字符串，无法统一进 ErrCode：原始值总是带出去，
	// 只有本身是十进制整数时才同时给出数字形态，使两条通道的 errcode 同义。
	result := provider.Result{
		UpstreamCode: failure.Code,
		ErrMsg:       errMsg,
		Class:        classify(resp.StatusCode, failure.Code+" "+failure.Message),
	}
	if parsed, err := strconv.ParseInt(failure.Code, 10, 64); err == nil {
		result.ErrCode = parsed
	}
	if result.Class == provider.ClassThrottled {
		result.RetryAfter = throttleRetryAfter
	}
	return result
}

func (p *Provider) fetchToken(ctx context.Context) (string, time.Time, error) {
	payload, err := json.Marshal(tokenRequest{AppKey: p.cfg.ClientID, AppSecret: p.cfg.ClientSecret})
	if err != nil {
		return "", time.Time{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("parse token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("token endpoint returned an empty accessToken")
	}

	lifetime := time.Duration(parsed.ExpireIn) * time.Second
	if lifetime <= 0 {
		lifetime = 2 * time.Hour
	}
	return parsed.AccessToken, p.cfg.Now().Add(lifetime), nil
}

type sendRequest struct {
	MsgParam           string `json:"msgParam"`
	MsgKey             string `json:"msgKey"`
	OpenConversationID string `json:"openConversationId"`
	RobotCode          string `json:"robotCode"`
}

type tokenRequest struct {
	AppKey    string `json:"appKey"`
	AppSecret string `json:"appSecret"`
}

type tokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireIn    int64  `json:"expireIn"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Request string `json:"requestid"`
}

// classify 按状态码与错误码对上游结果分类。
//
// 限流排在鉴权之前：企业通道的 403 既可能是权限问题也可能是 QPS 问题，
// 而 QpsLimit 这类片段足够特异，先判限流不会误伤 InvalidAuthentication。
func classify(status int, code string) provider.ErrorClass {
	lower := strings.ToLower(code)
	switch {
	case isThrottled(status, lower):
		return provider.ClassThrottled
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		strings.Contains(lower, "invalidauthentication"), strings.Contains(lower, "forbidden"):
		return provider.ClassAuth
	case status >= 500:
		return provider.ClassRetryable
	default:
		return provider.ClassRejected
	}
}

// isThrottled 判定服务端 API 的基础频率限制。
//
// 90002/90006/90018/90019 是钉钉的限流数字码（全局维度、每分钟、每秒），
// 新版接口则用 QpsLimit 之类的 code 片段表达；两者都可能只出现在 message 里。
func isThrottled(status int, lowerCode string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	for _, code := range []string{"90002", "90006", "90018", "90019"} {
		if strings.Contains(lowerCode, code) {
			return true
		}
	}
	for _, fragment := range []string{"qpslimit", "throttling", "flowcontrol", "ratelimit", "toomanyrequests"} {
		if strings.Contains(lowerCode, fragment) {
			return true
		}
	}
	return false
}

func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}
	return string(body)
}

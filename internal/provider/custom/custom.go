// Package custom 实现钉钉自定义机器人 Webhook 通道（存量通道）。
package custom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
	"dingtalk-robot/pkg/dingtalk"
)

// DefaultEndpoint 是自定义机器人 Webhook 的默认地址。
const DefaultEndpoint = "https://oapi.dingtalk.com/robot/send"

const maxResponseBytes = 1 << 20

// Config 是通道配置。
type Config struct {
	AccessToken string
	Secret      string
	Client      *http.Client
	Now         func() time.Time
}

// Provider 是自定义机器人通道。
type Provider struct {
	cfg Config
}

// New 创建通道。
func New(cfg Config) *Provider {
	if cfg.Client == nil {
		cfg.Client = provider.NewHTTPClient(0)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{cfg: cfg}
}

// Name 返回通道名。
func (p *Provider) Name() string { return "custom" }

// Validate 声明自定义机器人通道支持全部五种消息类型，因此没有额外的能力限制。
//
// 与通道无关的可投递性校验（正文非空、markdown 标题、@ 上限、正文长度等）由
// relay 在调用通道前统一完成，这里不需要重复实现。
func (p *Provider) Validate(*message.Message) error { return nil }

// Send 向钉钉投递一条消息（单次尝试，签名每次重新计算）。
func (p *Provider) Send(ctx context.Context, m *message.Message) provider.Result {
	payload, err := Payload(m)
	if err != nil {
		return provider.Result{Class: provider.ClassRejected, ErrMsg: err.Error()}
	}

	endpoint, err := SignedURL(DefaultEndpoint, p.cfg.AccessToken, p.cfg.Secret, p.cfg.Now())
	if err != nil {
		return provider.Result{Class: provider.ClassConfig, ErrMsg: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return provider.Result{Class: provider.ClassConfig, ErrMsg: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Charset", "utf8")

	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return provider.Result{Class: provider.ClassRetryable, ErrMsg: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return provider.Result{Class: provider.ClassRetryable, ErrMsg: err.Error()}
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		class := provider.ClassRejected
		if resp.StatusCode >= 500 {
			class = provider.ClassRetryable
		}
		return provider.Result{
			Class:  class,
			ErrMsg: fmt.Sprintf("unparseable response (HTTP %d): %s", resp.StatusCode, snippet(body)),
		}
	}

	code := parsed.code()
	class := classifyCode(code)
	if class == provider.ClassOK && resp.StatusCode >= http.StatusBadRequest {
		class = classifyHTTP(resp.StatusCode)
	}

	result := provider.Result{
		ErrCode: code,
		ErrMsg:  parsed.ErrMsg,
		Class:   class,
	}
	if class == provider.ClassThrottled {
		// 官方超限后会封禁 10 分钟，等待建议必须与其对齐。
		result.RetryAfter = provider.CustomThrottleRetryAfter
	}
	return result
}

// Payload 把规范化消息序列化为自定义机器人报文。
func Payload(m *message.Message) ([]byte, error) {
	switch m.Type {
	case message.TypeText:
		out := dingtalk.TextMessage{Text: dingtalk.Text{Content: m.Text}}
		if m.HasAt() {
			out.At = atOf(m.At)
		}
		return json.Marshal(out)

	case message.TypeMarkdown:
		out := dingtalk.MarkdownMessage{
			Markdown: dingtalk.Markdown{Title: m.Title, Text: m.Text},
		}
		if m.HasAt() {
			out.At = atOf(m.At)
		}
		return json.Marshal(out)

	case message.TypeLink:
		return json.Marshal(dingtalk.LinkMessage{
			Link: dingtalk.Link{
				Title:      m.Link.Title,
				Text:       m.Link.Text,
				PicURL:     m.Link.PicURL,
				MessageURL: m.Link.MessageURL,
			},
		})

	case message.TypeActionCard:
		card := dingtalk.ActionCard{
			Title:          m.Card.Title,
			Text:           m.Card.Text,
			SingleTitle:    m.Card.SingleTitle,
			SingleURL:      m.Card.SingleURL,
			BtnOrientation: m.Card.BtnOrientation,
			HideAvatar:     m.Card.HideAvatar,
		}
		for _, btn := range m.Card.Buttons {
			card.Btns = append(card.Btns, dingtalk.Btn{Title: btn.Title, ActionURL: btn.ActionURL})
		}
		return json.Marshal(dingtalk.ActionCardMessage{ActionCard: card})

	case message.TypeFeedCard:
		links := make([]dingtalk.FeedCardLink, 0, len(m.Feed))
		for _, link := range m.Feed {
			links = append(links, dingtalk.FeedCardLink{
				Title:      link.Title,
				PicURL:     link.PicURL,
				MessageURL: link.MessageURL,
			})
		}
		return json.Marshal(dingtalk.FeedCardMessage{FeedCard: dingtalk.FeedCard{Links: links}})
	}

	return nil, fmt.Errorf("unsupported message type %q", m.Type)
}

func atOf(at message.At) *dingtalk.At {
	return &dingtalk.At{
		AtMobiles: at.Mobiles,
		AtUserIDs: at.UserIDs,
		IsAtAll:   at.IsAtAll,
	}
}

// apiResponse 兼容 errcode 为数字或字符串两种历史形态。
type apiResponse struct {
	ErrCode json.Number `json:"errcode"`
	ErrMsg  string      `json:"errmsg"`
}

func (r apiResponse) code() int64 {
	raw := r.ErrCode.String()
	if raw == "" {
		return 0
	}
	parsed, err := r.ErrCode.Int64()
	if err != nil {
		return 0
	}
	return parsed
}

// classifyCode 把上游 errcode 映射为错误分类。
func classifyCode(code int64) provider.ErrorClass {
	switch code {
	case 0:
		return provider.ClassOK
	case 410100, 130101, 45009:
		// 发送过快或配额耗尽：重试同一份额度只会延长封禁与响应时间。
		return provider.ClassThrottled
	case -1:
		return provider.ClassRetryable
	case 310000, 400101, 400102, 400106:
		// 签名/关键词/IP/机器人凭据类问题，属于部署配置错误。
		return provider.ClassConfig
	case 400013:
		return provider.ClassRejected
	}
	if code >= 500 && code < 600 {
		return provider.ClassRetryable
	}
	return provider.ClassRejected
}

// classifyHTTP 在响应体缺少 errcode 时按状态码分类。
func classifyHTTP(status int) provider.ErrorClass {
	switch {
	case status == http.StatusTooManyRequests:
		return provider.ClassThrottled
	case status >= 500:
		return provider.ClassRetryable
	default:
		return provider.ClassRejected
	}
}

func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}
	return string(body)
}

// Package message 定义与投递通道无关的规范化消息模型。
package message

import (
	"fmt"
	"strings"

	"dingtalk-robot/internal/apierr"
)

// Type 是消息类型。
type Type string

// 支持的消息类型，取值与钉钉报文的 msgtype 保持一致。
const (
	TypeText       Type = "text"
	TypeLink       Type = "link"
	TypeMarkdown   Type = "markdown"
	TypeActionCard Type = "actionCard"
	TypeFeedCard   Type = "feedCard"
)

// SupportedTypes 返回入口接受的全部消息类型。
func SupportedTypes() []Type {
	return []Type{TypeText, TypeLink, TypeMarkdown, TypeActionCard, TypeFeedCard}
}

// MaxAtMentions 是 atMobiles 与 atUserIds 合计的上限（官方限制）。
const MaxAtMentions = 50

// MaxTextBytes 是消息正文的长度上限，与官方 msgParam 限制保持一致。
const MaxTextBytes = 15000

// At 描述被 @ 的群成员。
type At struct {
	Mobiles []string
	UserIDs []string
	IsAtAll bool
}

// Count 返回 @ 的成员总数（isAtAll 不计入官方上限口径）。
func (a At) Count() int { return len(a.Mobiles) + len(a.UserIDs) }

// Empty 报告是否未指定任何 @ 信息。
func (a At) Empty() bool {
	return len(a.Mobiles) == 0 && len(a.UserIDs) == 0 && !a.IsAtAll
}

// Link 是 link 类型的消息体。
type Link struct {
	Title      string
	Text       string
	PicURL     string
	MessageURL string
}

// Button 是 actionCard 的按钮。
type Button struct {
	Title     string
	ActionURL string
}

// ActionCard 是 actionCard 类型的消息体。
type ActionCard struct {
	Title          string
	Text           string
	SingleTitle    string
	SingleURL      string
	Buttons        []Button
	BtnOrientation string
	HideAvatar     string
}

// FeedLink 是 feedCard 的单条信息。
type FeedLink struct {
	Title      string
	PicURL     string
	MessageURL string
}

// Message 是与通道无关的规范化消息。
type Message struct {
	Type  Type
	Title string
	Text  string
	At    At
	Link  Link
	Card  ActionCard
	Feed  []FeedLink
}

// HasAt 报告消息是否携带 @ 信息。
func (m *Message) HasAt() bool { return !m.At.Empty() }

// FillMarkdownMentions 为 markdown 消息补齐正文中缺失的 @手机号。
//
// 钉钉只在 markdown 正文出现 @手机号 时才真正 @ 到人，仅带 at 字段不会生效。
// 这里把 atMobiles 里尚未出现在正文中的手机号追加到末尾，使 @ 不再静默失效；
// 已出现的不重复追加，因此本方法是幂等的。返回追加的手机号个数。
//
// atUserIds 与 isAtAll 不改写正文：官方对 markdown 只要求 @手机号 的形式，
// 而正文没有内容时保持原样，交给 Validate 报"正文为空"。
func (m *Message) FillMarkdownMentions() int {
	if m == nil || m.Type != TypeMarkdown || len(m.At.Mobiles) == 0 {
		return 0
	}
	if strings.TrimSpace(m.Text) == "" {
		return 0
	}

	missing := make([]string, 0, len(m.At.Mobiles))
	seen := make(map[string]struct{}, len(m.At.Mobiles))
	for _, mobile := range m.At.Mobiles {
		mobile = strings.TrimSpace(mobile)
		if mobile == "" {
			continue
		}
		if _, duplicate := seen[mobile]; duplicate {
			continue
		}
		seen[mobile] = struct{}{}
		if strings.Contains(m.Text, "@"+mobile) {
			continue
		}
		missing = append(missing, "@"+mobile)
	}
	if len(missing) == 0 {
		return 0
	}

	m.Text = strings.TrimRight(m.Text, " \t\r\n") + "\n\n" + strings.Join(missing, " ")
	return len(missing)
}

// Validate 校验消息是否可投递。返回的错误携带稳定 code。
func (m *Message) Validate() error {
	if m == nil {
		return apierr.InvalidMessage("message is required")
	}

	switch m.Type {
	case TypeText, TypeMarkdown:
		if strings.TrimSpace(m.Text) == "" {
			return apierr.InvalidMessage(fmt.Sprintf("%s message requires non-empty text", m.Type))
		}
		if m.Type == TypeMarkdown && strings.TrimSpace(m.Title) == "" {
			return apierr.InvalidMessage("markdown message requires non-empty title")
		}
	case TypeLink:
		if strings.TrimSpace(m.Link.Title) == "" {
			return apierr.InvalidMessage("link message requires non-empty title")
		}
		if strings.TrimSpace(m.Link.Text) == "" {
			return apierr.InvalidMessage("link message requires non-empty text")
		}
		if strings.TrimSpace(m.Link.MessageURL) == "" {
			return apierr.InvalidMessage("link message requires non-empty messageUrl")
		}
	case TypeActionCard:
		if strings.TrimSpace(m.Card.Title) == "" {
			return apierr.InvalidMessage("actionCard message requires non-empty title")
		}
		if strings.TrimSpace(m.Card.Text) == "" {
			return apierr.InvalidMessage("actionCard message requires non-empty text")
		}
		if err := m.validateActionCardButtons(); err != nil {
			return err
		}
	case TypeFeedCard:
		if len(m.Feed) == 0 {
			return apierr.InvalidMessage("feedCard message requires at least one link")
		}
	default:
		return apierr.New(400, apierr.CodeUnsupportedMessageType,
			fmt.Sprintf("unsupported msgtype %q, supported: %s", m.Type, joinTypes()))
	}

	if m.HasAt() && m.Type != TypeText && m.Type != TypeMarkdown {
		return apierr.New(400, apierr.CodeAtNotSupported,
			fmt.Sprintf("at is only supported for text and markdown messages, got %q", m.Type))
	}

	if n := m.At.Count(); n > MaxAtMentions {
		return apierr.New(400, apierr.CodeAtLimitExceeded,
			fmt.Sprintf("at mentions limit exceeded: %d > %d", n, MaxAtMentions))
	}

	if n := len(m.body()); n > MaxTextBytes {
		return apierr.New(400, apierr.CodeMessageTooLarge,
			fmt.Sprintf("message body too large: %d > %d bytes", n, MaxTextBytes))
	}

	return nil
}

// validateActionCardButtons 保证卡片至少有一种可渲染的跳转方式。
func (m *Message) validateActionCardButtons() error {
	buttons := m.Card.Buttons
	if len(buttons) == 0 {
		if strings.TrimSpace(m.Card.SingleTitle) == "" || strings.TrimSpace(m.Card.SingleURL) == "" {
			return apierr.InvalidMessage("actionCard requires either btns or singleTitle+singleURL")
		}
		return nil
	}

	for i, btn := range buttons {
		if strings.TrimSpace(btn.Title) == "" || strings.TrimSpace(btn.ActionURL) == "" {
			return apierr.InvalidMessage(
				fmt.Sprintf("actionCard button #%d requires non-empty title and actionURL", i+1))
		}
	}
	return nil
}

// body 返回参与长度限制的文本内容。
func (m *Message) body() string {
	switch m.Type {
	case TypeMarkdown:
		return m.Text
	case TypeLink:
		return m.Link.Text
	case TypeActionCard:
		return m.Card.Text
	default:
		return m.Text
	}
}

func joinTypes() string {
	types := SupportedTypes()
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}
	return strings.Join(out, ", ")
}

// Package ingest 把入站 HTTP 载荷解析为规范化消息。
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/pkg/dingtalk"
)

// Parse 接受两种载荷：原生钉钉报文（含 msgtype）与简化纯文本体（text + 可选 title）。
func Parse(body []byte) (*message.Message, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, apierr.InvalidJSON("request body is empty")
	}
	if trimmed[0] != '{' {
		return nil, apierr.InvalidJSON("request body must be a JSON object")
	}

	var probe struct {
		MsgType string          `json:"msgtype"`
		Text    json.RawMessage `json:"text"`
		Title   string          `json:"title"`
		At      json.RawMessage `json:"at"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, apierr.InvalidJSON(fmt.Sprintf("invalid JSON: %v", err))
	}

	if probe.MsgType == "" {
		return parseSimplified(probe.Title, probe.Text, probe.At)
	}

	switch dingtalk.MsgType(probe.MsgType) {
	case dingtalk.MsgTypeText:
		var wire dingtalk.TextMessage
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, apierr.InvalidJSON(fmt.Sprintf("invalid text message: %v", err))
		}
		return &message.Message{
			Type: message.TypeText,
			Text: wire.Text.Content,
			At:   toAt(wire.At),
		}, nil

	case dingtalk.MsgTypeMarkdown:
		var wire dingtalk.MarkdownMessage
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, apierr.InvalidJSON(fmt.Sprintf("invalid markdown message: %v", err))
		}
		msg := &message.Message{
			Type:  message.TypeMarkdown,
			Title: wire.Markdown.Title,
			Text:  wire.Markdown.Text,
			At:    toAt(wire.At),
		}
		// 钉钉只在正文出现 @手机号 时才 @ 到人，补齐发生在返回之前，
		// 因此后续的正文长度校验按补齐后的内容判定。
		msg.FillMarkdownMentions()
		return msg, nil

	case dingtalk.MsgTypeLink:
		if err := rejectAt(probe.At, "link"); err != nil {
			return nil, err
		}
		var wire dingtalk.LinkMessage
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, apierr.InvalidJSON(fmt.Sprintf("invalid link message: %v", err))
		}
		return &message.Message{
			Type: message.TypeLink,
			Link: message.Link{
				Title:      wire.Link.Title,
				Text:       wire.Link.Text,
				PicURL:     wire.Link.PicURL,
				MessageURL: wire.Link.MessageURL,
			},
		}, nil

	case dingtalk.MsgTypeActionCard:
		if err := rejectAt(probe.At, "actionCard"); err != nil {
			return nil, err
		}
		var wire dingtalk.ActionCardMessage
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, apierr.InvalidJSON(fmt.Sprintf("invalid actionCard message: %v", err))
		}
		buttons := make([]message.Button, 0, len(wire.ActionCard.Btns))
		for _, btn := range wire.ActionCard.Btns {
			buttons = append(buttons, message.Button{Title: btn.Title, ActionURL: btn.ActionURL})
		}
		return &message.Message{
			Type: message.TypeActionCard,
			Card: message.ActionCard{
				Title:          wire.ActionCard.Title,
				Text:           wire.ActionCard.Text,
				SingleTitle:    wire.ActionCard.SingleTitle,
				SingleURL:      wire.ActionCard.SingleURL,
				Buttons:        buttons,
				BtnOrientation: wire.ActionCard.BtnOrientation,
				HideAvatar:     wire.ActionCard.HideAvatar,
			},
		}, nil

	case dingtalk.MsgTypeFeedCard:
		if err := rejectAt(probe.At, "feedCard"); err != nil {
			return nil, err
		}
		var wire dingtalk.FeedCardMessage
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, apierr.InvalidJSON(fmt.Sprintf("invalid feedCard message: %v", err))
		}
		links := make([]message.FeedLink, 0, len(wire.FeedCard.Links))
		for _, link := range wire.FeedCard.Links {
			links = append(links, message.FeedLink{
				Title:      link.Title,
				PicURL:     link.PicURL,
				MessageURL: link.MessageURL,
			})
		}
		return &message.Message{Type: message.TypeFeedCard, Feed: links}, nil
	}

	return nil, apierr.New(400, apierr.CodeUnsupportedMessageType,
		fmt.Sprintf("unsupported msgtype %q, supported: %s", probe.MsgType, supportedList()))
}

// parseSimplified 处理 {"title":"...","text":"...","at":{...}} 形式的简化载荷。
//
// at 与原生报文里的 at 语义一致：简化体固定产出 text 消息，因此 @ 一定生效。
func parseSimplified(title string, rawText, rawAt json.RawMessage) (*message.Message, error) {
	if len(rawText) == 0 || bytes.Equal(bytes.TrimSpace(rawText), []byte("null")) {
		return nil, apierr.InvalidRequest("text is required")
	}

	var text string
	if err := json.Unmarshal(rawText, &text); err != nil {
		return nil, apierr.InvalidRequest("text must be a string")
	}

	content := text
	if t := strings.TrimSpace(title); t != "" {
		content = fmt.Sprintf("%s\n\n%s", t, text)
	}

	at, err := parseAt(rawAt)
	if err != nil {
		return nil, err
	}

	return &message.Message{Type: message.TypeText, Text: content, At: at}, nil
}

// parseAt 解析可选的 at 对象；省略或 null 视为没有 @ 信息。
func parseAt(raw json.RawMessage) (message.At, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return message.At{}, nil
	}

	var wire dingtalk.At
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return message.At{}, apierr.InvalidRequest(
			"at must be an object with atMobiles, atUserIds and isAtAll")
	}
	return toAt(&wire), nil
}

// rejectAt 拒绝在非 text/markdown 类型上携带 @，避免调用方误以为 @ 生效。
func rejectAt(raw json.RawMessage, msgType string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return apierr.New(400, apierr.CodeAtNotSupported,
		fmt.Sprintf("at is only supported for text and markdown messages, got %q", msgType))
}

func toAt(at *dingtalk.At) message.At {
	if at == nil {
		return message.At{}
	}
	return message.At{
		Mobiles: at.AtMobiles,
		UserIDs: at.AtUserIDs,
		IsAtAll: at.IsAtAll,
	}
}

func supportedList() string {
	types := message.SupportedTypes()
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}
	return strings.Join(out, ", ")
}

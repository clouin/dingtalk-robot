package ingest

import (
	"encoding/json"
	"strings"
	"testing"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/message"
)

func TestParseNativeMessages(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, m *message.Message)
	}{
		{
			name: "text with at",
			body: `{"msgtype":"text","text":{"content":"hello"},"at":{"atMobiles":["13800000000"],"atUserIds":["u1"],"isAtAll":true}}`,
			check: func(t *testing.T, m *message.Message) {
				if m.Type != message.TypeText || m.Text != "hello" {
					t.Fatalf("unexpected message: %+v", m)
				}
				if len(m.At.Mobiles) != 1 || len(m.At.UserIDs) != 1 || !m.At.IsAtAll {
					t.Errorf("at not preserved: %+v", m.At)
				}
			},
		},
		{
			name: "markdown",
			body: `{"msgtype":"markdown","markdown":{"title":"task","text":"### done"}}`,
			check: func(t *testing.T, m *message.Message) {
				if m.Type != message.TypeMarkdown || m.Title != "task" || m.Text != "### done" {
					t.Fatalf("unexpected message: %+v", m)
				}
			},
		},
		{
			name: "link",
			body: `{"msgtype":"link","link":{"title":"t","text":"x","picUrl":"https://p","messageUrl":"https://u"}}`,
			check: func(t *testing.T, m *message.Message) {
				if m.Type != message.TypeLink || m.Link.MessageURL != "https://u" || m.Link.PicURL != "https://p" {
					t.Fatalf("unexpected message: %+v", m)
				}
			},
		},
		{
			name: "actionCard with buttons",
			body: `{"msgtype":"actionCard","actionCard":{"title":"t","text":"x","btnOrientation":"1","hideAvatar":"0","btns":[{"title":"a","actionURL":"https://a"},{"title":"b","actionURL":"https://b"}]}}`,
			check: func(t *testing.T, m *message.Message) {
				if m.Type != message.TypeActionCard {
					t.Fatalf("unexpected type: %s", m.Type)
				}
				if len(m.Card.Buttons) != 2 || m.Card.Buttons[1].Title != "b" {
					t.Errorf("buttons not preserved: %+v", m.Card.Buttons)
				}
				if m.Card.BtnOrientation != "1" || m.Card.HideAvatar != "0" {
					t.Errorf("presentation fields not preserved: %+v", m.Card)
				}
			},
		},
		{
			name: "actionCard overall jump",
			body: `{"msgtype":"actionCard","actionCard":{"title":"t","text":"x","singleTitle":"open","singleURL":"https://a"}}`,
			check: func(t *testing.T, m *message.Message) {
				if m.Card.SingleTitle != "open" || m.Card.SingleURL != "https://a" {
					t.Fatalf("single jump fields not preserved: %+v", m.Card)
				}
			},
		},
		{
			name: "feedCard",
			body: `{"msgtype":"feedCard","feedCard":{"links":[{"title":"a","messageURL":"https://a","picURL":"https://p"}]}}`,
			check: func(t *testing.T, m *message.Message) {
				if len(m.Feed) != 1 || m.Feed[0].MessageURL != "https://a" {
					t.Fatalf("links not preserved: %+v", m.Feed)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := Parse([]byte(tc.body))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			tc.check(t, msg)
		})
	}
}

func TestParseSimplifiedBody(t *testing.T) {
	msg, err := Parse([]byte(`{"text":"简单文本"}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if msg.Type != message.TypeText || msg.Text != "简单文本" {
		t.Fatalf("unexpected message: %+v", msg)
	}

	msg, err = Parse([]byte(`{"title":"标题","text":"正文"}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if msg.Text != "标题\n\n正文" {
		t.Errorf("text = %q, want title and body merged", msg.Text)
	}
}

func TestParseSimplifiedKeepsAt(t *testing.T) {
	msg, err := Parse([]byte(`{"text":"hello","at":{"atMobiles":["150XXXXXXXX"]}}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if msg.Type != message.TypeText || msg.Text != "hello" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if len(msg.At.Mobiles) != 1 || msg.At.Mobiles[0] != "150XXXXXXXX" {
		t.Errorf("atMobiles not preserved: %+v", msg.At)
	}

	msg, err = Parse([]byte(`{"text":"hello","at":{"isAtAll":true}}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !msg.At.IsAtAll {
		t.Errorf("isAtAll not preserved: %+v", msg.At)
	}

	msg, err = Parse([]byte(`{"text":"hello","at":null}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !msg.At.Empty() {
		t.Errorf("null at should stay empty, got %+v", msg.At)
	}
}

func TestParseSimplifiedAtLimitIsEnforced(t *testing.T) {
	mobiles := make([]string, message.MaxAtMentions+1)
	for i := range mobiles {
		mobiles[i] = "15000000000"
	}
	body, err := json.Marshal(map[string]any{
		"text": "hello",
		"at":   map[string]any{"atMobiles": mobiles},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	msg, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	apiErr, ok := apierr.As(msg.Validate())
	if !ok {
		t.Fatal("Validate returned nil error for too many mentions")
	}
	if apiErr.Code != apierr.CodeAtLimitExceeded {
		t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeAtLimitExceeded)
	}
	if apiErr.Status != 400 {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
}

func TestParseMarkdownFillsMentions(t *testing.T) {
	msg, err := Parse([]byte(`{"msgtype":"markdown","markdown":{"title":"t","text":"### done"},"at":{"atMobiles":["150XXXXXXXX"]}}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if want := "### done\n\n@150XXXXXXXX"; msg.Text != want {
		t.Errorf("text = %q, want %q", msg.Text, want)
	}
}

func TestParseMarkdownMentionsCountTowardBodySize(t *testing.T) {
	text := strings.Repeat("a", message.MaxTextBytes-10)
	body := `{"msgtype":"markdown","markdown":{"title":"t","text":"` + text + `"},"at":{"atMobiles":["150XXXXXXXX"]}}`

	msg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	apiErr, ok := apierr.As(msg.Validate())
	if !ok {
		t.Fatal("Validate returned nil error, want the mention to push the body over the limit")
	}
	if apiErr.Code != apierr.CodeMessageTooLarge {
		t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeMessageTooLarge)
	}

	// 对照：正文本身没超限，是补齐后的内容超了。
	if len(text) >= message.MaxTextBytes {
		t.Fatalf("test setup: base text already exceeds the limit")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "empty body", body: ``, code: apierr.CodeInvalidJSON},
		{name: "not an object", body: `"hello"`, code: apierr.CodeInvalidJSON},
		{name: "broken json", body: `{"text":`, code: apierr.CodeInvalidJSON},
		{name: "unknown msgtype", body: `{"msgtype":"image"}`, code: apierr.CodeUnsupportedMessageType},
		{name: "unknown msgtype with text", body: `{"msgtype":"image","text":"x"}`, code: apierr.CodeUnsupportedMessageType},
		{name: "missing text", body: `{"title":"t"}`, code: apierr.CodeInvalidRequest},
		{
			name: "link with at",
			body: `{"msgtype":"link","link":{"title":"t","text":"x","messageUrl":"https://u"},"at":{"atMobiles":["13800000000"]}}`,
			code: apierr.CodeAtNotSupported,
		},
		{
			name: "actionCard with at",
			body: `{"msgtype":"actionCard","actionCard":{"title":"t","text":"x","btns":[{"title":"a","actionURL":"https://a"}]},"at":{"isAtAll":true}}`,
			code: apierr.CodeAtNotSupported,
		},
		{name: "text is not a string", body: `{"text":{"content":"x"}}`, code: apierr.CodeInvalidRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			apiErr, ok := apierr.As(err)
			if !ok {
				t.Fatalf("Parse returned %v (%T), want *apierr.Error", err, err)
			}
			if apiErr.Code != tc.code {
				t.Errorf("code = %q, want %q", apiErr.Code, tc.code)
			}
			if apiErr.Status != 400 {
				t.Errorf("status = %d, want 400", apiErr.Status)
			}
		})
	}
}

func TestParseUnknownMsgTypeListsSupportedTypes(t *testing.T) {
	_, err := Parse([]byte(`{"msgtype":"image"}`))
	apiErr, _ := apierr.As(err)
	for _, want := range []string{"text", "markdown", "link", "actionCard", "feedCard"} {
		if !strings.Contains(apiErr.Message, want) {
			t.Errorf("error %q does not list %q", apiErr.Message, want)
		}
	}
}

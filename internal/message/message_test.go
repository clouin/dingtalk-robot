package message

import (
	"strings"
	"testing"

	"dingtalk-robot/internal/apierr"
)

func TestValidateText(t *testing.T) {
	tests := []struct {
		name    string
		msg     Message
		wantErr bool
		code    string
	}{
		{
			name: "valid text",
			msg:  Message{Type: TypeText, Text: "hello"},
		},
		{
			name:    "empty text",
			msg:     Message{Type: TypeText, Text: "   "},
			wantErr: true,
			code:    apierr.CodeInvalidMessage,
		},
		{
			name:    "markdown without title",
			msg:     Message{Type: TypeMarkdown, Text: "# hi"},
			wantErr: true,
			code:    apierr.CodeInvalidMessage,
		},
		{
			name:    "link without message url",
			msg:     Message{Type: TypeLink, Link: Link{Title: "t", Text: "x"}},
			wantErr: true,
			code:    apierr.CodeInvalidMessage,
		},
		{
			name:    "feed card without links",
			msg:     Message{Type: TypeFeedCard},
			wantErr: true,
			code:    apierr.CodeInvalidMessage,
		},
		{
			name:    "unknown type",
			msg:     Message{Type: Type("image"), Text: "x"},
			wantErr: true,
			code:    apierr.CodeUnsupportedMessageType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.Validate()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			apiErr, ok := apierr.As(err)
			if !ok {
				t.Fatalf("Validate returned %T, want *apierr.Error", err)
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

func TestValidateAtMentions(t *testing.T) {
	mobiles := make([]string, MaxAtMentions)
	for i := range mobiles {
		mobiles[i] = "13800000000"
	}

	atLimit := Message{Type: TypeText, Text: "hi", At: At{Mobiles: mobiles[:MaxAtMentions-1]}}
	atLimit.At.UserIDs = []string{"user1"}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("exactly %d mentions should be allowed, got %v", MaxAtMentions, err)
	}

	overLimit := Message{Type: TypeText, Text: "hi", At: At{Mobiles: mobiles}}
	overLimit.At.UserIDs = []string{"user1"}
	err := overLimit.Validate()
	apiErr, ok := apierr.As(err)
	if !ok {
		t.Fatalf("Validate returned %T, want *apierr.Error", err)
	}
	if apiErr.Code != apierr.CodeAtLimitExceeded {
		t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeAtLimitExceeded)
	}
}

func TestAtOnlyForTextAndMarkdown(t *testing.T) {
	cases := map[string]Message{
		"link": {
			Type: TypeLink,
			Link: Link{Title: "t", Text: "x", MessageURL: "https://u"},
		},
		"actionCard": {
			Type: TypeActionCard,
			Card: ActionCard{Title: "t", Text: "x", SingleTitle: "a", SingleURL: "https://u"},
		},
		"feedCard": {
			Type: TypeFeedCard,
			Feed: []FeedLink{{Title: "t", MessageURL: "https://u"}},
		},
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			msg.At = At{Mobiles: []string{"13800000000"}}
			apiErr, ok := apierr.As(msg.Validate())
			if !ok {
				t.Fatalf("%s: Validate returned nil, want error", name)
			}
			if apiErr.Code != apierr.CodeAtNotSupported {
				t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeAtNotSupported)
			}
		})
	}
}

func TestActionCardRequiresRenderableTarget(t *testing.T) {
	tests := []struct {
		name    string
		card    ActionCard
		wantErr bool
	}{
		{
			name:    "no target at all",
			card:    ActionCard{Title: "t", Text: "x"},
			wantErr: true,
		},
		{
			name: "single jump pair",
			card: ActionCard{Title: "t", Text: "x", SingleTitle: "open", SingleURL: "https://u"},
		},
		{
			name: "buttons",
			card: ActionCard{Title: "t", Text: "x", Buttons: []Button{{Title: "a", ActionURL: "https://u"}}},
		},
		{
			name:    "button without url",
			card:    ActionCard{Title: "t", Text: "x", Buttons: []Button{{Title: "a"}}},
			wantErr: true,
		},
		{
			name:    "single title without url",
			card:    ActionCard{Title: "t", Text: "x", SingleTitle: "open"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{Type: TypeActionCard, Card: tc.card}
			err := msg.Validate()

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			apiErr, ok := apierr.As(err)
			if !ok {
				t.Fatalf("Validate returned %v, want *apierr.Error", err)
			}
			if apiErr.Code != apierr.CodeInvalidMessage {
				t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeInvalidMessage)
			}
		})
	}
}

func TestValidateBodySize(t *testing.T) {
	msg := Message{Type: TypeText, Text: strings.Repeat("a", MaxTextBytes+1)}
	apiErr, ok := apierr.As(msg.Validate())
	if !ok {
		t.Fatal("Validate returned nil error for oversized body")
	}
	if apiErr.Code != apierr.CodeMessageTooLarge {
		t.Errorf("code = %q, want %q", apiErr.Code, apierr.CodeMessageTooLarge)
	}
}

func TestFillMarkdownMentions(t *testing.T) {
	tests := []struct {
		name      string
		msg       Message
		wantText  string
		wantAdded int
	}{
		{
			name:      "appends missing mobile",
			msg:       Message{Type: TypeMarkdown, Text: "### done", At: At{Mobiles: []string{"150XXXXXXXX"}}},
			wantText:  "### done\n\n@150XXXXXXXX",
			wantAdded: 1,
		},
		{
			name:      "keeps body that already mentions the mobile",
			msg:       Message{Type: TypeMarkdown, Text: "hi @150XXXXXXXX", At: At{Mobiles: []string{"150XXXXXXXX"}}},
			wantText:  "hi @150XXXXXXXX",
			wantAdded: 0,
		},
		{
			name:      "appends only the missing one of two",
			msg:       Message{Type: TypeMarkdown, Text: "hi @150XXXXXXXX", At: At{Mobiles: []string{"150XXXXXXXX", "151YYYYYYYY"}}},
			wantText:  "hi @150XXXXXXXX\n\n@151YYYYYYYY",
			wantAdded: 1,
		},
		{
			name:      "ignores atUserIds and isAtAll",
			msg:       Message{Type: TypeMarkdown, Text: "body", At: At{UserIDs: []string{"u1"}, IsAtAll: true}},
			wantText:  "body",
			wantAdded: 0,
		},
		{
			name:      "leaves text messages alone",
			msg:       Message{Type: TypeText, Text: "body", At: At{Mobiles: []string{"150XXXXXXXX"}}},
			wantText:  "body",
			wantAdded: 0,
		},
		{
			name:      "leaves blank bodies alone",
			msg:       Message{Type: TypeMarkdown, Text: "   ", At: At{Mobiles: []string{"150XXXXXXXX"}}},
			wantText:  "   ",
			wantAdded: 0,
		},
		{
			name:      "skips blank mobiles",
			msg:       Message{Type: TypeMarkdown, Text: "body", At: At{Mobiles: []string{"  "}}},
			wantText:  "body",
			wantAdded: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.FillMarkdownMentions(); got != tc.wantAdded {
				t.Errorf("added = %d, want %d", got, tc.wantAdded)
			}
			if tc.msg.Text != tc.wantText {
				t.Errorf("text = %q, want %q", tc.msg.Text, tc.wantText)
			}
			// 幂等：再调用一次不产生任何改写。
			if got := tc.msg.FillMarkdownMentions(); got != 0 {
				t.Errorf("second call added %d mentions, want 0", got)
			}
			if tc.msg.Text != tc.wantText {
				t.Errorf("second call changed text to %q, want %q", tc.msg.Text, tc.wantText)
			}
		})
	}
}

func TestAtEmpty(t *testing.T) {
	if !(At{}).Empty() {
		t.Error("zero At should be empty")
	}
	if (At{IsAtAll: true}).Empty() {
		t.Error("At with isAtAll should not be empty")
	}
	if got := (At{Mobiles: []string{"1"}, UserIDs: []string{"2"}}).Count(); got != 2 {
		t.Errorf("Count = %d, want 2", got)
	}
}

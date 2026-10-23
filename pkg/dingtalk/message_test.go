package dingtalk

import (
	"encoding/json"
	"testing"
)

// decode 把序列化结果解成 map，便于断言字段名与结构。
func decode(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestTextMessageWireFormat(t *testing.T) {
	msg := TextMessage{
		Text: Text{Content: "hi"},
		At:   &At{AtMobiles: []string{"13800000000"}, AtUserIDs: []string{"u1"}, IsAtAll: true},
	}

	got := decode(t, msg)

	if got["msgtype"] != "text" {
		t.Errorf("msgtype = %v, want text", got["msgtype"])
	}
	text, ok := got["text"].(map[string]any)
	if !ok || text["content"] != "hi" {
		t.Errorf("text.content not encoded as expected: %v", got["text"])
	}
	at, ok := got["at"].(map[string]any)
	if !ok {
		t.Fatalf("at missing: %v", got)
	}
	if at["isAtAll"] != true {
		t.Errorf("at.isAtAll = %v, want true", at["isAtAll"])
	}
	if _, ok := at["atUserIds"]; !ok {
		t.Errorf("at.atUserIds missing: %v", at)
	}
	if _, ok := at["atMobiles"]; !ok {
		t.Errorf("at.atMobiles missing: %v", at)
	}

	// 序列化不得修改接收者。
	if msg.MsgType != "" {
		t.Errorf("MarshalJSON mutated receiver MsgType to %q", msg.MsgType)
	}
}

func TestOmitsEmptyAt(t *testing.T) {
	got := decode(t, TextMessage{Text: Text{Content: "hi"}})
	if _, ok := got["at"]; ok {
		t.Errorf("at should be omitted when nil: %v", got)
	}
}

func TestMarkdownMessageWireFormat(t *testing.T) {
	got := decode(t, MarkdownMessage{Markdown: Markdown{Title: "t", Text: "# h"}})
	if got["msgtype"] != "markdown" {
		t.Errorf("msgtype = %v, want markdown", got["msgtype"])
	}
	section, ok := got["markdown"].(map[string]any)
	if !ok || section["title"] != "t" || section["text"] != "# h" {
		t.Errorf("markdown section unexpected: %v", got["markdown"])
	}
}

func TestLinkMessageWireFormat(t *testing.T) {
	got := decode(t, LinkMessage{Link: Link{Title: "t", Text: "x", PicURL: "p", MessageURL: "u"}})
	if got["msgtype"] != "link" {
		t.Errorf("msgtype = %v, want link", got["msgtype"])
	}
	section, ok := got["link"].(map[string]any)
	if !ok {
		t.Fatalf("link section missing: %v", got)
	}
	if section["picUrl"] != "p" || section["messageUrl"] != "u" {
		t.Errorf("camelCase field names expected, got %v", section)
	}
}

func TestActionCardMessageWireFormat(t *testing.T) {
	got := decode(t, ActionCardMessage{ActionCard: ActionCard{
		Title:          "t",
		Text:           "x",
		Btns:           []Btn{{Title: "a", ActionURL: "https://a"}},
		BtnOrientation: "1",
		HideAvatar:     "0",
	}})

	if got["msgtype"] != "actionCard" {
		t.Errorf("msgtype = %v, want actionCard", got["msgtype"])
	}
	section, ok := got["actionCard"].(map[string]any)
	if !ok {
		t.Fatalf("actionCard section missing: %v", got)
	}
	btns, ok := section["btns"].([]any)
	if !ok || len(btns) != 1 {
		t.Fatalf("btns unexpected: %v", section["btns"])
	}
	btn := btns[0].(map[string]any)
	if btn["actionURL"] != "https://a" {
		t.Errorf("btn actionURL = %v, want https://a", btn["actionURL"])
	}
}

func TestFeedCardMessageWireFormat(t *testing.T) {
	got := decode(t, FeedCardMessage{FeedCard: FeedCard{Links: []FeedCardLink{
		{Title: "t", PicURL: "p", MessageURL: "u"},
	}}})

	if got["msgtype"] != "feedCard" {
		t.Errorf("msgtype = %v, want feedCard", got["msgtype"])
	}
	section, ok := got["feedCard"].(map[string]any)
	if !ok {
		t.Fatalf("feedCard section missing: %v", got)
	}
	links, ok := section["links"].([]any)
	if !ok || len(links) != 1 {
		t.Fatalf("links unexpected: %v", section["links"])
	}
	link := links[0].(map[string]any)
	if link["messageURL"] != "u" || link["picURL"] != "p" {
		t.Errorf("feedCard uses messageURL/picURL, got %v", link)
	}
}

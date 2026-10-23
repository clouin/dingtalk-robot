package dingtalk

import "encoding/json"

// MarkdownMessage 是 markdown 类型消息。
type MarkdownMessage struct {
	MsgType  MsgType  `json:"msgtype"`
	Markdown Markdown `json:"markdown"`
	At       *At      `json:"at,omitempty"`
}

// Markdown 是 markdown 消息体。
type Markdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// MarshalJSON 序列化并强制写入 msgtype，不修改接收者。
func (m MarkdownMessage) MarshalJSON() ([]byte, error) {
	m.MsgType = MsgTypeMarkdown
	type wire MarkdownMessage
	return json.Marshal(wire(m))
}

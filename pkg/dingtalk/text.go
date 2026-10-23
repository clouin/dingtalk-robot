package dingtalk

import "encoding/json"

// TextMessage 是 text 类型消息。
type TextMessage struct {
	MsgType MsgType `json:"msgtype"`
	Text    Text    `json:"text"`
	At      *At     `json:"at,omitempty"`
}

// Text 是 text 消息体。
type Text struct {
	Content string `json:"content"`
}

// MarshalJSON 序列化并强制写入 msgtype，不修改接收者。
func (m TextMessage) MarshalJSON() ([]byte, error) {
	m.MsgType = MsgTypeText
	type wire TextMessage
	return json.Marshal(wire(m))
}

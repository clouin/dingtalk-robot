package dingtalk

import "encoding/json"

// LinkMessage 是 link 类型消息。link 不支持 at。
type LinkMessage struct {
	MsgType MsgType `json:"msgtype"`
	Link    Link    `json:"link"`
}

// Link 是 link 消息体。
type Link struct {
	Title      string `json:"title"`
	Text       string `json:"text"`
	PicURL     string `json:"picUrl,omitempty"`
	MessageURL string `json:"messageUrl"`
}

// MarshalJSON 序列化并强制写入 msgtype，不修改接收者。
func (m LinkMessage) MarshalJSON() ([]byte, error) {
	m.MsgType = MsgTypeLink
	type wire LinkMessage
	return json.Marshal(wire(m))
}

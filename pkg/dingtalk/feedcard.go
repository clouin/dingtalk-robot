package dingtalk

import "encoding/json"

// FeedCardMessage 是 feedCard 类型消息。
type FeedCardMessage struct {
	MsgType  MsgType  `json:"msgtype"`
	FeedCard FeedCard `json:"feedCard"`
}

// FeedCard 是 feedCard 消息体。
type FeedCard struct {
	Links []FeedCardLink `json:"links"`
}

// FeedCardLink 是 feedCard 的单条信息。注意字段名与 link 类型不同（picURL/messageURL）。
type FeedCardLink struct {
	Title      string `json:"title"`
	PicURL     string `json:"picURL"`
	MessageURL string `json:"messageURL"`
}

// MarshalJSON 序列化并强制写入 msgtype，不修改接收者。
func (m FeedCardMessage) MarshalJSON() ([]byte, error) {
	m.MsgType = MsgTypeFeedCard
	type wire FeedCardMessage
	return json.Marshal(wire(m))
}

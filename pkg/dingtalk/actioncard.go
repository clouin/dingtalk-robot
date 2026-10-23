package dingtalk

import "encoding/json"

// ActionCardMessage 是 actionCard 类型消息，同时支持整体跳转与独立跳转。
type ActionCardMessage struct {
	MsgType    MsgType    `json:"msgtype"`
	ActionCard ActionCard `json:"actionCard"`
}

// ActionCard 是 actionCard 消息体。
type ActionCard struct {
	Title          string `json:"title"`
	Text           string `json:"text"`
	SingleTitle    string `json:"singleTitle,omitempty"`
	SingleURL      string `json:"singleURL,omitempty"`
	Btns           []Btn  `json:"btns,omitempty"`
	BtnOrientation string `json:"btnOrientation,omitempty"`
	HideAvatar     string `json:"hideAvatar,omitempty"`
}

// Btn 是独立跳转按钮。
type Btn struct {
	Title     string `json:"title"`
	ActionURL string `json:"actionURL"`
}

// MarshalJSON 序列化并强制写入 msgtype，不修改接收者。
func (m ActionCardMessage) MarshalJSON() ([]byte, error) {
	m.MsgType = MsgTypeActionCard
	type wire ActionCardMessage
	return json.Marshal(wire(m))
}

// Package dingtalk 定义钉钉自定义机器人 Webhook 的消息报文结构。
//
// 本包只负责报文形状：所有类型都是纯数据，序列化不修改接收者。
// 网络请求、签名与重试由 internal/provider 负责。
package dingtalk

// MsgType 是钉钉消息类型。
type MsgType string

// 钉钉自定义机器人支持的消息类型。
const (
	MsgTypeText       MsgType = "text"
	MsgTypeMarkdown   MsgType = "markdown"
	MsgTypeLink       MsgType = "link"
	MsgTypeActionCard MsgType = "actionCard"
	MsgTypeFeedCard   MsgType = "feedCard"
)

// At 描述被 @ 的群成员。atMobiles 与 atUserIds 合计不超过 50 人。
type At struct {
	AtMobiles []string `json:"atMobiles,omitempty"`
	AtUserIDs []string `json:"atUserIds,omitempty"`
	IsAtAll   bool     `json:"isAtAll,omitempty"`
}

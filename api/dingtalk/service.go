package dingtalk

import (
	"encoding/json"
	"fmt"

	"dingtalk-robot/configs"
	"dingtalk-robot/pkg/dingtalk"
)

const (
	HTTP_INTERNAL_ERROR  = 400001
	HTTP_PARAMETER_ERROR = 400002
)

var debug = configs.Debug

func newClient() (*dingtalk.Client, error) {
	token := configs.GetAccessToken()
	secret := configs.GetSecret()

	if len(token) < 1 {
		return nil, fmt.Errorf("%s can not be empty", configs.AccessTokenKey)
	}
	client := dingtalk.NewClient(token, secret)
	return client, nil
}

func Request(msgType string, body []byte) dingtalk.Response {
	var resp dingtalk.Response
	switch dingtalk.MsgType(msgType) {
	case dingtalk.MsgTypeText:
		var reqText textParams
		json.Unmarshal(body, &reqText)
		resp = sendText(reqText)
	case dingtalk.MsgTypeLink:
		var reqLink linkParams
		json.Unmarshal(body, &reqLink)
		resp = sendLink(reqLink)
	case dingtalk.MsgTypeMarkdown:
		var reqMarkdown markdownParams
		json.Unmarshal(body, &reqMarkdown)
		resp = sendMarkdown(reqMarkdown)
	case dingtalk.MsgTypeActionCard:
		var reqActionCard actionCardParams
		json.Unmarshal(body, &reqActionCard)
		resp = sendActionCard(reqActionCard)
	case dingtalk.MsgTypeFeedCard:
		var reqFeedCard feedCardParams
		json.Unmarshal(body, &reqFeedCard)
		resp = sendFeedCard(reqFeedCard)
	default:
		resp = dingtalk.Response{
			ErrCode: HTTP_PARAMETER_ERROR,
			ErrMsg:  "parameter error",
		}
	}

	return resp
}

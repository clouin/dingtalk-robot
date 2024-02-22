package robot

import (
	"encoding/json"
	"fmt"

	"dingtalk-robot/config"
	"dingtalk-robot/pkg/dingtalk"

	log "github.com/sirupsen/logrus"
)

const (
	HTTP_INTERNAL_ERROR  = 400001
	HTTP_PARAMETER_ERROR = 400002
)

func newClient() (*dingtalk.Client, error) {
	token := config.Content.DingTalk.AccessToken
	secret := config.Content.DingTalk.Secret

	if token == "" {
		err := fmt.Errorf("dingtalk access_token is required")
		log.Warn(err)
		return nil, err
	}
	if secret == "" {
		err := fmt.Errorf("dingtalk secret is required")
		log.Warn(err)
		return nil, err
	}
	client := dingtalk.NewClient(token, secret)
	return client, nil
}

func Request(jsonBytes []byte) dingtalk.Response {
	client, err := newClient()
	if err != nil {
		return dingtalk.Response{
			ErrCode: HTTP_INTERNAL_ERROR,
			ErrMsg:  err.Error(),
		}
	}

	var reqString string
	resp := &dingtalk.Response{}

	var req map[string]string
	//解析json编码的数据并将结果存入req指向的值
	json.Unmarshal(jsonBytes, &req)
	log.Debugf("request body: %+v", req)

	msgType := dingtalk.MsgType(req["msgtype"])
	switch msgType {
	case dingtalk.MsgTypeText:
		var req dingtalk.TextMessage
		json.Unmarshal(jsonBytes, &req)
		reqString, resp, err = client.Send(&req)
	case dingtalk.MsgTypeLink:
		var req dingtalk.LinkMessage
		json.Unmarshal(jsonBytes, &req)
		reqString, resp, err = client.Send(&req)
	case dingtalk.MsgTypeMarkdown:
		var req dingtalk.MarkdownMessage
		json.Unmarshal(jsonBytes, &req)
		reqString, resp, err = client.Send(&req)
	case dingtalk.MsgTypeActionCard:
		var req dingtalk.ActionCardMessage
		json.Unmarshal(jsonBytes, &req)
		reqString, resp, err = client.Send(&req)
	case dingtalk.MsgTypeFeedCard:
		var req dingtalk.FeedCardMessage
		json.Unmarshal(jsonBytes, &req)
		reqString, resp, err = client.Send(&req)
	default:
		text := req["text"]
		title := req["title"]
		if title != "" {
			text = fmt.Sprintf("%s\n\n%s", title, text)
		}
		if text != "" {
			msg := dingtalk.NewTextMessage().SetContent(text)
			reqString, resp, err = client.Send(msg)
		} else {
			resp = &dingtalk.Response{
				ErrCode: HTTP_PARAMETER_ERROR,
				ErrMsg:  "parameter error",
			}
		}
	}
	log.Debugf("request string: %s", reqString)
	if err != nil {
		log.Errorf("request error: %s", err)
	}

	return *resp
}

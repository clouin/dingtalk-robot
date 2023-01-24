package dingtalk

import (
	"log"

	"dingtalk-robot/pkg/dingtalk"
)

type textParams struct {
	MsgType   dingtalk.MsgType `json:"msgtype"`
	Content   string           `json:"content"`
	AtMobiles []string         `json:"atMobiles"`
	IsAtAll   bool             `json:"isAtAll"`
}

func sendText(req textParams) dingtalk.Response {
	if len(req.Content) == 0 {
		errorMsg := "content can not be empty"
		log.Print(errorMsg)
		return dingtalk.Response{
			ErrCode: HTTP_PARAMETER_ERROR,
			ErrMsg:  errorMsg,
		}
	}

	client, err := newClient()
	if err != nil {
		log.Print(err.Error())
		return dingtalk.Response{
			ErrCode: HTTP_INTERNAL_ERROR,
			ErrMsg:  err.Error(),
		}
	}

	msg := dingtalk.NewTextMessage().SetContent(req.Content).SetAt(req.AtMobiles, req.IsAtAll)
	reqString, res, err := client.Send(msg)
	if debug {
		log.Print(reqString)
	}
	if err != nil {
		log.Print(err.Error())
	}
	return *res
}

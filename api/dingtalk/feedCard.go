package dingtalk

import (
	"log"

	"dingtalk-robot/pkg/dingtalk"
)

type feedCardParams struct {
	MsgType dingtalk.MsgType        `json:"msgtype"`
	Links   []dingtalk.FeedCardLink `json:"links"`
}

func sendFeedCard(req feedCardParams) dingtalk.Response {
	if len(req.Links) == 0 {
		errorMsg := "links can not be empty"
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

	msg := dingtalk.NewFeedCardMessage().AppendLinks(req.Links)
	reqString, res, err := client.Send(msg)
	if debug {
		log.Print(reqString)
	}
	if err != nil {
		log.Print(err.Error())
	}
	return *res
}

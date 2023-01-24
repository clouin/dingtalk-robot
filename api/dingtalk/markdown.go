package dingtalk

import (
	"log"

	"dingtalk-robot/pkg/dingtalk"
)

type markdownParams struct {
	MsgType   dingtalk.MsgType `json:"msgtype"`
	Title     string           `json:"title"`
	Text      string           `json:"text"`
	AtMobiles []string         `json:"atMobiles"`
	IsAtAll   bool             `json:"isAtAll"`
}

func sendMarkdown(req markdownParams) dingtalk.Response {
	if len(req.Title) == 0 || len(req.Text) == 0 {
		errorMsg := "title|text can not be empty"
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

	msg := dingtalk.NewMarkdownMessage().SetMarkdown(req.Title, req.Text).SetAt(req.AtMobiles, req.IsAtAll)
	reqString, res, err := client.Send(msg)
	if debug {
		log.Print(reqString)
	}
	if err != nil {
		log.Print(err.Error())
	}
	return *res
}

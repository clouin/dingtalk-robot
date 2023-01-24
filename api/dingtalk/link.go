package dingtalk

import (
	"log"

	"dingtalk-robot/pkg/dingtalk"
)

type linkParams struct {
	MsgType    dingtalk.MsgType `json:"msgtype"`
	Title      string           `json:"title"`
	Text       string           `json:"text"`
	PicURL     string           `json:"picUrl"`
	MessageURL string           `json:"messageUrl"`
}

func sendLink(req linkParams) dingtalk.Response {
	if len(req.Title) == 0 || len(req.Text) == 0 || len(req.MessageURL) == 0 {
		errorMsg := "title|text|messageUrl can not be empty"
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

	msg := dingtalk.NewLinkMessage().SetLink(req.Title, req.Text, req.PicURL, req.MessageURL)
	reqString, res, err := client.Send(msg)
	if debug {
		log.Print(reqString)
	}
	if err != nil {
		log.Print(err.Error())
	}
	return *res
}

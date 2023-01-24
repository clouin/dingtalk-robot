package dingtalk

import (
	"log"

	"dingtalk-robot/pkg/dingtalk"
)

type actionCardParams struct {
	MsgType        dingtalk.MsgType `json:"msgtype"`
	Title          string           `json:"title"`
	Text           string           `json:"text"`
	SingleTitle    string           `json:"singleTitle"`
	SingleURL      string           `json:"singleURL"`
	Btns           []dingtalk.Btn   `json:"btns"`
	BtnOrientation string           `json:"btnOrientation"`
	HideAvatar     string           `json:"hideAvatar"`
}

func sendActionCard(req actionCardParams) dingtalk.Response {
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

	var msg *dingtalk.ActionCardMessage
	if len(req.Btns) > 0 {
		msg = dingtalk.NewActionCardMessage().SetIndependentJump(req.Title, req.Text, req.Btns, req.BtnOrientation, req.HideAvatar)
	} else {
		msg = dingtalk.NewActionCardMessage().SetOverallJump(req.Title, req.Text, req.SingleTitle, req.SingleURL, req.BtnOrientation, req.HideAvatar)
	}

	reqString, res, err := client.Send(msg)
	if debug {
		log.Print(reqString)
	}
	if err != nil {
		log.Print(err.Error())
	}
	return *res
}

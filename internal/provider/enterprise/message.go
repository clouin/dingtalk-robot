package enterprise

import (
	"encoding/json"
	"fmt"

	"dingtalk-robot/internal/message"
)

// buildMessage 把规范化消息映射为钉钉消息模板（msgKey + msgParam）。
func buildMessage(m *message.Message) (string, []byte, error) {
	switch m.Type {
	case message.TypeText:
		return template("sampleText", textParam{Content: m.Text})
	case message.TypeMarkdown:
		return template("sampleMarkdown", markdownParam{Title: m.Title, Text: m.Text})
	case message.TypeLink:
		return template("sampleLink", linkParam{
			Title:      m.Link.Title,
			Text:       m.Link.Text,
			PicURL:     m.Link.PicURL,
			MessageURL: m.Link.MessageURL,
		})
	case message.TypeActionCard:
		return buildActionCard(m.Card)
	}
	return "", nil, fmt.Errorf("message type %q cannot be mapped to an enterprise robot template", m.Type)
}

func buildActionCard(card message.ActionCard) (string, []byte, error) {
	buttons := card.Buttons
	switch {
	case len(buttons) == 0:
		return template("sampleActionCard", actionCardParam{
			Title:       card.Title,
			Text:        card.Text,
			SingleTitle: card.SingleTitle,
			SingleURL:   card.SingleURL,
		})
	case len(buttons) == 1:
		return template("sampleActionCard", actionCardParam{
			Title:       card.Title,
			Text:        card.Text,
			SingleTitle: buttons[0].Title,
			SingleURL:   buttons[0].ActionURL,
		})
	case len(buttons) == 2 && card.BtnOrientation == "1":
		return template("sampleActionCard6", horizontalActionCardParam{
			Title:        card.Title,
			Text:         card.Text,
			ButtonTitle1: buttons[0].Title,
			ButtonURL1:   buttons[0].ActionURL,
			ButtonTitle2: buttons[1].Title,
			ButtonURL2:   buttons[1].ActionURL,
		})
	case len(buttons) <= maxActionCardButtons:
		param := multiActionCardParam{Title: card.Title, Text: card.Text}
		for i, btn := range buttons {
			param.set(i, btn)
		}
		return template(fmt.Sprintf("sampleActionCard%d", len(buttons)), param)
	}
	return "", nil, fmt.Errorf("actionCard supports at most %d buttons, got %d",
		maxActionCardButtons, len(buttons))
}

func marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal message param: %w", err)
	}
	return data, nil
}

// template 组合 msgKey 与序列化后的 msgParam。
func template(key string, v any) (string, []byte, error) {
	data, err := marshal(v)
	if err != nil {
		return "", nil, err
	}
	return key, data, nil
}

type textParam struct {
	Content string `json:"content"`
}

type markdownParam struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type linkParam struct {
	Title      string `json:"title"`
	Text       string `json:"text"`
	PicURL     string `json:"picUrl,omitempty"`
	MessageURL string `json:"messageUrl"`
}

type actionCardParam struct {
	Title       string `json:"title"`
	Text        string `json:"text"`
	SingleTitle string `json:"singleTitle,omitempty"`
	SingleURL   string `json:"singleURL,omitempty"`
}

type horizontalActionCardParam struct {
	Title        string `json:"title"`
	Text         string `json:"text"`
	ButtonTitle1 string `json:"buttonTitle1"`
	ButtonURL1   string `json:"buttonUrl1"`
	ButtonTitle2 string `json:"buttonTitle2"`
	ButtonURL2   string `json:"buttonUrl2"`
}

type multiActionCardParam struct {
	Title        string `json:"title"`
	Text         string `json:"text"`
	ActionTitle1 string `json:"actionTitle1,omitempty"`
	ActionURL1   string `json:"actionURL1,omitempty"`
	ActionTitle2 string `json:"actionTitle2,omitempty"`
	ActionURL2   string `json:"actionURL2,omitempty"`
	ActionTitle3 string `json:"actionTitle3,omitempty"`
	ActionURL3   string `json:"actionURL3,omitempty"`
	ActionTitle4 string `json:"actionTitle4,omitempty"`
	ActionURL4   string `json:"actionURL4,omitempty"`
	ActionTitle5 string `json:"actionTitle5,omitempty"`
	ActionURL5   string `json:"actionURL5,omitempty"`
}

// set 按索引填充第 i 个按钮（从 0 开始）。
func (p *multiActionCardParam) set(i int, btn message.Button) {
	titles := []*string{&p.ActionTitle1, &p.ActionTitle2, &p.ActionTitle3, &p.ActionTitle4, &p.ActionTitle5}
	urls := []*string{&p.ActionURL1, &p.ActionURL2, &p.ActionURL3, &p.ActionURL4, &p.ActionURL5}
	if i < 0 || i >= len(titles) {
		return
	}
	*titles[i] = btn.Title
	*urls[i] = btn.ActionURL
}

package diun

import (
	"encoding/json"
	"fmt"
	"strings"

	"dingtalk-robot/config"
	"dingtalk-robot/pkg/dingtalk"

	log "github.com/sirupsen/logrus"
)

// DiunEvent represents the structure of a diun webhook event
type DiunEvent struct {
	DiunVersion string                 `json:"diun_version"`
	Hostname    string                 `json:"hostname"`
	Status      string                 `json:"status"`
	Provider    string                 `json:"provider"`
	Image       string                 `json:"image"`
	HubLink     string                 `json:"hub_link"`
	MimeType    string                 `json:"mime_type"`
	Digest      string                 `json:"digest"`
	Created     string                 `json:"created"`
	Platform    string                 `json:"platform"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// ProcessDiunEvent processes a diun webhook event and sends notification to DingTalk
func ProcessDiunEvent(eventBytes []byte) dingtalk.Response {
	// Parse the diun event
	var event DiunEvent
	if err := json.Unmarshal(eventBytes, &event); err != nil {
		log.Errorf("Failed to parse diun event: %v", err)
		return dingtalk.Response{
			ErrCode: 400001,
			ErrMsg:  "Failed to parse diun event",
		}
	}

	// Create markdown message
	markdownText := formatDiunMessage(event)
	msg := dingtalk.NewMarkdownMessage().SetMarkdown(
		"Diun Image Update Notification",
		markdownText,
	)

	// Send to DingTalk
	client, err := newDingTalkClient()
	if err != nil {
		return dingtalk.Response{
			ErrCode: 400001,
			ErrMsg:  err.Error(),
		}
	}

	reqString, resp, err := client.Send(msg)
	log.Debugf("DingTalk request: %s", reqString)
	if err != nil {
		log.Errorf("Failed to send DingTalk message: %v", err)
		return *resp
	}

	log.Infof("Successfully sent DingTalk notification for image: %s", event.Image)
	return *resp
}

// formatDiunMessage formats the diun event into a markdown message
func formatDiunMessage(event DiunEvent) string {
	var metadataStr string
	if len(event.Metadata) > 0 {
		metadataStr = "\n\n**Metadata:**\n"
		for key, value := range event.Metadata {
			metadataStr += fmt.Sprintf("- %s: %v\n", key, value)
		}
	}

	// Determine emoji based on status
	var statusEmoji string
	switch strings.ToLower(event.Status) {
	case "new":
		statusEmoji = "🆕"
	case "update":
		statusEmoji = "🔄"
	case "removed":
		statusEmoji = "🗑️"
	default:
		statusEmoji = "ℹ️"
	}

	return fmt.Sprintf(`### %s Diun Image Update Notification

- **Hostname:** %s
- **Provider:** %s
- **Created:** %s
- **Digest:** %s
- **Platform:** %s
- **Status:** %s
- **Image:** %s
- **Hub Link:** %s

%s`,
		statusEmoji,
		event.Hostname,
		event.Provider,
		event.Created,
		event.Digest,
		event.Platform,
		event.Status,
		event.Image,
		event.HubLink,
		metadataStr)
}

// newDingTalkClient creates a new DingTalk client using configuration
func newDingTalkClient() (*dingtalk.Client, error) {
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

	return dingtalk.NewClient(token, secret), nil
}

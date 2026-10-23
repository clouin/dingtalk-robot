// 离线辅助工具：通过钉钉 Stream 模式接收机器人消息与事件，打印群的 open_conversation_id。
// 不参与镜像构建，也不随镜像发布；用法见 docs/channels.md。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
	"gopkg.in/yaml.v3"
)

func main() {
	once := flag.Bool("once", true, "取到第一个 open_conversation_id 后退出；设为 false 则持续监听")
	configPath := flag.String("config", "", "配置文件路径；留空则按 config.yaml、config/config.yaml、../config/config.yaml 顺序自动查找")
	flag.Parse()

	// 凭据来源与主程序一致：配置文件 → 环境变量（环境变量优先）。
	clientID, clientSecret, source := resolveCredential(*configPath)
	if clientID == "" || clientSecret == "" {
		fmt.Fprintf(os.Stderr, `缺少企业应用凭据：配置文件里 dingtalk.enterprise.client_id / client_secret 为空，环境变量 DINGTALK_CLIENT_ID / DINGTALK_CLIENT_SECRET 也未设置。
当前来源：%s

usage: go run . [-config /data/code/dingtalk-robot/config/config.yaml] [-once=false]
`, source)
		os.Exit(2)
	}
	fmt.Printf("凭据来源：%s\n", source)

	logger.SetLogger(stdLogger{})

	cli := client.NewStreamClient(
		client.WithAppCredential(client.NewAppCredentialConfig(clientID, clientSecret)),
	)

	notify := make(chan struct{}, 1)
	wake := func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	// 机器人消息：群里 @ 一次机器人即可触发。
	cli.RegisterChatBotCallbackRouter(func(ctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		fmt.Printf("[bot ] msgtype=%s sender=%q title=%q conversationType=%s\n      conversationId=%s\n",
			data.Msgtype, data.SenderNick, data.ConversationTitle, data.ConversationType, data.ConversationId)
		if data.ConversationId != "" {
			printResult("机器人消息事件", data.ConversationId, data.ConversationTitle)
			wake()
		}
		return []byte(""), nil
	})

	// 群相关事件（改标题、加人、机器人被拉群等）里也常带会话 ID，一并兜住。
	cli.RegisterAllEventRouter(func(ctx context.Context, df *payload.DataFrame) (*payload.DataFrameResponse, error) {
		fmt.Printf("[event] topic=%s data=%s\n", df.GetTopic(), truncate(df.Data, 1200))
		var v any
		if err := json.Unmarshal([]byte(df.Data), &v); err == nil {
			for _, id := range collectIDs(v) {
				printResult("事件 "+df.GetTopic(), id, lookupTitle(v))
				wake()
				break
			}
		}
		return payload.NewSuccessDataFrameResponse(), nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cli.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("connected. 现在到目标群里 @ 一次机器人（随便发一句话）...")

	for {
		select {
		case <-ctx.Done():
			cli.Close()
			return
		case <-notify:
			if *once {
				cli.Close()
				return
			}
		}
	}
}

func printResult(source, id, title string) {
	line := strings.Repeat("=", 64)
	fmt.Printf("\n%s\n 来源: %s\n open_conversation_id: %s\n 群名称: %s\n%s\n", line, source, id, title, line)
	fmt.Printf(" 填到 config/config.yaml: dingtalk.enterprise.open_conversation_id: %q\n\n", id)
}

// collectIDs 递归找出 json 里键名形如 conversationId / openConversationId 的字符串值。
func collectIDs(v any) []string {
	var ids []string
	var walk func(any)
	walk = func(node any) {
		switch t := node.(type) {
		case map[string]any:
			for k, val := range t {
				if s, ok := val.(string); ok && s != "" {
					lk := strings.ToLower(k)
					if lk == "conversationid" || lk == "openconversationid" {
						ids = append(ids, s)
					}
				}
				walk(val)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return ids
}

func lookupTitle(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, k := range []string{"conversationTitle", "title", "name"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// fileConfig 只取本工具需要的两个字段，避免为了一次性用途引入主模块的完整校验。
type fileConfig struct {
	DingTalk struct {
		Enterprise struct {
			ClientID     string `yaml:"client_id"`
			ClientSecret string `yaml:"client_secret"`
		} `yaml:"enterprise"`
	} `yaml:"dingtalk"`
}

// resolveCredential 读取企业应用凭据，语义与主程序 config.Load 对齐：文件为底，环境变量覆盖。
func resolveCredential(explicit string) (clientID, clientSecret, source string) {
	path := explicit
	if path == "" {
		path = discoverConfig()
	}

	var fc fileConfig
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取配置文件 %s 失败: %v\n", path, err)
		} else if err := yaml.Unmarshal(data, &fc); err != nil {
			fmt.Fprintf(os.Stderr, "解析配置文件 %s 失败: %v\n", path, err)
		}
	}

	clientID, clientSecret = fc.DingTalk.Enterprise.ClientID, fc.DingTalk.Enterprise.ClientSecret
	idFrom, secretFrom := path, path
	if v := os.Getenv("DINGTALK_CLIENT_ID"); v != "" {
		clientID, idFrom = v, "env"
	}
	if v := os.Getenv("DINGTALK_CLIENT_SECRET"); v != "" {
		clientSecret, secretFrom = v, "env"
	}

	label := func(from string) string {
		if from == "" {
			return "无"
		}
		return from
	}
	source = fmt.Sprintf("client_id ← %s, client_secret ← %s", label(idFrom), label(secretFrom))
	return clientID, clientSecret, source
}

// discoverConfig 从当前目录逐层向上查找配置文件。
// 工具既可能在 tools/getcid/ 下运行，也可能被复制到别处，因此不假设固定的目录层级；
// 一直走到根目录时，容器里的 /config/config.yaml 也会被命中。
func discoverConfig() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 5; i++ {
		for _, rel := range []string{"config.yaml", filepath.Join("config", "config.yaml")} {
			candidate := filepath.Join(dir, rel)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

type stdLogger struct{}

func (stdLogger) Debugf(string, ...interface{}) {}

func (stdLogger) Infof(f string, a ...interface{}) { log.Printf("[sdk] "+f, a...) }

func (stdLogger) Warningf(f string, a ...interface{}) { log.Printf("[sdk] "+f, a...) }

func (stdLogger) Errorf(f string, a ...interface{}) { log.Printf("[sdk] "+f, a...) }

func (stdLogger) Fatalf(f string, a ...interface{}) { log.Fatalf("[sdk] "+f, a...) }

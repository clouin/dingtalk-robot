# 投递通道

服务只有一个投递目标，由 `provider` 在 `custom`（自定义机器人 Webhook）与 `enterprise`（企业内部应用机器人）之间二选一。本页讲选型、凭据、能力差异与上游失败特征；报文字段见 [接口契约](api.md)，配置键见 [配置](configuration.md)。

## 选型

- 只想把一个群接上、五分钟内跑通：选 `custom`，一条 webhook 地址就够。
- 需要企业内部应用的凭据体系（凭据不落在群里）、或无法在群里创建机器人：选 `enterprise`，代价是四个必填值加一次性的应用侧准备。

## `custom`：自定义机器人

### 凭据

| 配置键                         | 环境变量                | 从哪来                                                     |
| ------------------------------ | ----------------------- | ---------------------------------------------------------- |
| `dingtalk.custom.access_token` | `DINGTALK_ACCESS_TOKEN` | 群机器人 Webhook 地址里 `access_token=` 后面的值           |
| `dingtalk.custom.secret`       | `DINGTALK_SECRET`       | 机器人安全设置为「加签」时生成；用关键词或 IP 白名单时留空 |

`secret` 只影响签名：填了就对每次投递附加 `timestamp` 与 `sign`（HMAC-SHA256，密钥为 `secret`，消息为 `timestamp + "\n" + secret`，结果 Base64；`timestamp` 是毫秒）。留空则按非加签发送。是否加签以机器人侧的安全设置为准，两边必须一致，否则上游以 `310000` 拒绝。

官方文档：

- [自定义机器人接入](https://open.dingtalk.com/document/robots/custom-robot-access)（Webhook 与 `access_token`）
- [自定义机器人安全设置](https://open.dingtalk.com/document/robots/customize-robot-security-settings)（加签、关键词、IP 白名单）

### 配置与自检

```bash
PROVIDER=custom \
API_TOKEN=dev-token \
DINGTALK_ACCESS_TOKEN="<webhook token>" \
DINGTALK_SECRET="<加签 secret，非加签留空>" \
go run ./cmd/dingtalk-robot
```

```bash
curl -s -X POST localhost:8080/v1/messages \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"text":"hello"}'
```

响应里 `provider` 应为 `custom`、`errcode` 为 `0`。若返回 502 且 `errcode:310000`，按[上游失败特征](#上游失败特征)排查。

## `enterprise`：企业内部应用机器人

### 凭据

| 配置键                                     | 环境变量                        | 从哪来                        |
| ------------------------------------------ | ------------------------------- | ----------------------------- |
| `dingtalk.enterprise.client_id`            | `DINGTALK_CLIENT_ID`            | 企业内部应用的 Client ID      |
| `dingtalk.enterprise.client_secret`        | `DINGTALK_CLIENT_SECRET`        | 企业内部应用的 Client Secret  |
| `dingtalk.enterprise.robot_code`           | `DINGTALK_ROBOT_CODE`           | 该应用下机器人编码            |
| `dingtalk.enterprise.open_conversation_id` | `DINGTALK_OPEN_CONVERSATION_ID` | 目标群的会话 ID（`cid` 开头） |
| `dingtalk.enterprise.ignore_at`            | `DINGTALK_IGNORE_AT`            | 默认 `false`；见下            |

四项凭据缺任一项时进程拒绝启动，并在同一条错误里列出全部缺失项。`API_TOKEN` 同样必填。

`ignore_at`：企业通道的接口不支持 `@`。默认收到带 `at` 的消息返回 400 `at_not_supported`；置为 `true` 后丢弃 `at` 并正常投递，响应带 `at_dropped: true`，调用方能知道自己被「降级」了。

### 获取 `open_conversation_id`

钉钉客户端不显示这个值，只能从消息事件里取：让机器人收一次群消息，事件体的 `conversationId` 就是它。仓库里的 `tools/getcid` 已经把这一步做完了。

它只需要 `client_id` 与 `client_secret`。凭据来源与主程序一致（配置文件 → 环境变量，环境变量优先），配置文件按 `config.yaml`、`config/config.yaml`、`../config/config.yaml` 顺序查找：

```bash
cd tools/getcid

# 默认 -once=true：取到第一个 id 后退出
go run .

# 配置文件在别处
go run . -config /path/to/config.yaml

# 两者都没有时直接给环境变量
DINGTALK_CLIENT_ID="<client id>" DINGTALK_CLIENT_SECRET="<client secret>" go run .

# 持续监听，不自动退出
go run . -once=false
```

看到连接建立后，到目标群里 @ 一次机器人，终端会打印该群的 `open_conversation_id` 与群名。

**前提：机器人接收模式必须是 Stream。** 如果该机器人是 HTTP 回调模式，Stream 连接会建立成功但收不到任何消息——现象是终端停在连接成功之后再无输出。此时要么在开发者后台把接收模式改为 Stream，要么跳过工具：让现有回调服务接一次群消息，从日志里搜 `conversationId`，值是一样的。

这个工具是离线辅助脚本：独立 Go module，不参与镜像构建，也不随镜像发布。

**别混淆**：同一个群在不同地方有四个名字，前三个指向同一个值，`chatid` 是另一套编号。

| 出现位置                  | 名字                                                     | 形态       |
| ------------------------- | -------------------------------------------------------- | ---------- |
| 本服务的配置键 / 环境变量 | `open_conversation_id` / `DINGTALK_OPEN_CONVERSATION_ID` | `cid...`   |
| 钉钉接口返回体            | `openConversationId`                                     | 同上       |
| 机器人消息事件体          | `conversationId`                                         | 同上       |
| 群的会话 ID（旧字段）     | `chatid`                                                 | 另一套编号 |

配置里要的是前三行那个值。把 `chatid` 填进 `open_conversation_id`，启动不会报错，但投递会返回 400。

**手上正好有 `chatid` 时**（只有服务端创建群接口会返回它，前端 JSAPI 拿到的不接受）：可以调「获取群会话的 OpenConversationId」接口（`POST /v1.0/im/chat/{chatId}/convertToOpenConversationId`），返回体里的值即目标值，不必收消息。见[官方文档](https://open.dingtalk.com/document/orgapp-server/obtain-group-openconversationid)。

### 配置与自检

```bash
PROVIDER=enterprise \
API_TOKEN=dev-token \
DINGTALK_CLIENT_ID="<client id>" \
DINGTALK_CLIENT_SECRET="<client secret>" \
DINGTALK_ROBOT_CODE="<robot code>" \
DINGTALK_OPEN_CONVERSATION_ID="<cid...>" \
go run ./cmd/dingtalk-robot
```

启动日志里 `provider=enterprise` 表示通道已就绪。发送一条 `text` 自检：

```bash
curl -s -X POST localhost:8080/v1/messages \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"text":"hello"}'
```

企业通道首次投递会先换取访问令牌，之后复用缓存（到期前 5 分钟刷新）；令牌失效时服务会作废缓存并重试一次，因此偶发的上游 401 不会直接失败给调用方——但如果重试后仍是鉴权失败，最终返回 502。

官方文档：[机器人发送群聊消息（v1.0）](https://open.dingtalk.com/document/orgapp/the-robot-sends-a-group-message)。

## 能力差异

| 能力                             | `custom`                          | `enterprise`                                                                    |
| -------------------------------- | --------------------------------- | ------------------------------------------------------------------------------- |
| `text` / `link` / `markdown`     | 支持                              | 支持                                                                            |
| `actionCard`                     | 支持                              | 支持，最多 5 个按钮（超出 400 `option_not_supported`）                          |
| `feedCard`                       | 支持                              | 不支持，400 `unsupported_message_type_for_provider`                             |
| `at`（字段，非 `msgtype`）       | 仅 `text`/`markdown`，合计 ≤50 人 | 不支持；默认 400 `at_not_supported`，开启 `ignore_at` 后丢弃并标记 `at_dropped` |
| `hideAvatar="1"`                 | 原样透传                          | 不支持，400 `option_not_supported`                                              |
| 横向排列（`btnOrientation="1"`） | 原样透传                          | 仅允许 2 个按钮，超过返回 400                                                   |
| 正文上限                         | 15000 字节                        | 15000 字节                                                                      |
| 首条消息延迟                     | 直接投递                          | 先换访问令牌（之后复用缓存）                                                    |

`actionCard` 必须可渲染：给 `btns`（每个按钮需要 `title` 与 `actionURL`），或给成对的 `singleTitle` + `singleURL`。企业通道在 1 个按钮时映射为单按钮模板，2 个按钮且横向时映射为双按钮横向模板，其余按按钮数选择 2–5 按钮模板。

## 上游失败特征

| 现象                                                                                                        | 通道         | 分类与处置                                                                                               |
| ----------------------------------------------------------------------------------------------------------- | ------------ | -------------------------------------------------------------------------------------------------------- |
| 502 `upstream_rejected`、`errcode:310000`                                                                   | `custom`     | 配置类：加签 `secret` 不匹配、关键词未命中、IP 不在白名单。核对机器人安全设置与 `DINGTALK_SECRET`        |
| 502、`errcode` 为 `400101`/`400102`/`400106`                                                                | `custom`     | 配置类：机器人凭据/安全设置问题，重试无意义                                                              |
| 429 `rate_limited`、`Retry-After: 600`                                                                      | `custom`     | 触发每分钟 20 条上限并进入 10 分钟封禁。等封禁结束或下调 `rate_limit.per_minute`                         |
| 502 或 429、`errcode` 为 `410100`/`130101`/`45009`                                                          | `custom`     | 限流类：发送过快或配额耗尽。服务按 10 分钟给等待建议                                                     |
| 502、`errcode` 为 `-1`，或上游 5xx                                                                          | `custom`     | 可重试类：系统繁忙或网关错误，按 `send.retries` 退避重试                                                 |
| 502 且响应体是 HTML 页面                                                                                    | `custom`     | 上游返回了非 JSON（常见于 IP 维度限流封禁）。该维度没有业务码，等 5 分钟后重试                           |
| 429、`upstream_code` 为 `QpsLimit`/`Throttling`/`FlowControl`，或数字形态的 `90002`/`90006`/`90018`/`90019` | `enterprise` | 限流类：应用/接口/QPS 维度限流。`Retry-After: 1`，不必等 10 分钟；持续出现则下调 `rate_limit.per_minute` |
| 502、`upstream_code` 为 `InvalidAuthentication` 等                                                          | `enterprise` | 服务已作废缓存令牌并重试一次；仍失败说明 `client_id`/`client_secret` 或机器人授权有误                    |
| 502、`upstream_code` 为其他业务码                                                                           | `enterprise` | 拒绝类：内容审核、参数或权限问题，按 `errmsg` 处理                                                       |

企业通道的业务码是字符串，落在 `upstream_code`；`errcode` 只在业务码本身是数字时才有值。排障时以 `code` 判断类别、`upstream_code` 定位原因、`errmsg` 读原始描述，不要假设 `errcode` 一定有值。

## 一个实例只投递一条固定目标

`custom` 绑定 webhook 所属群，`enterprise` 绑定 `open_conversation_id`——两者都不是「可以随时指定群」。要发多个群就部署多个实例（各自配置不同的 webhook 或 `open_conversation_id`），再由同一入口按群分流到对应实例；服务本身不做多目标路由，也不做消息分发。

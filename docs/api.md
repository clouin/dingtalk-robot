# 接口契约

发送入口只有一个：`POST /v1/messages`。探针与指标端点见 [部署](deploy.md#探针) 与 [观测](operations.md#指标)。

## 入口

| 项目         | 要求                                                                                                            |
| ------------ | --------------------------------------------------------------------------------------------------------------- |
| 方法/路径    | `POST /v1/messages`                                                                                             |
| 鉴权         | `Authorization: Bearer <token>`，常量时间比较；`/healthz`、`/readyz` 免鉴权                                     |
| Content-Type | 必须是 `application/json`（允许附带 `charset`），否则 415                                                       |
| 请求体       | 必须是 JSON 对象；空体或非对象返回 400 `invalid_json`；默认上限 65536 字节（`server.max_body_bytes`），超限 413 |
| 匿名模式     | `server.allow_unauthenticated: true` 时完全不校验令牌                                                           |

令牌缺失或不匹配返回 401，响应带 `WWW-Authenticate: Bearer realm="dingtalk-robot"`。鉴权拒绝也会计入 `dingtalk_robot_requests_total{status="401"}`，令牌配错时能直接从指标看出来。

## 请求

请求体有两种写法，任选其一。

### 简化体

```json
{ "text": "hello" }
```

```json
{ "title": "示例标题", "text": "hello" }
```

```json
{ "text": "hello", "at": { "atMobiles": ["150XXXXXXXX"], "isAtAll": false } }
```

- `text` 必填且必须是字符串；缺失或为 `null` 返回 400 `invalid_request`。
- `title` 可选；与 `text` 同时提交时合并为 `标题` + 空行 + `正文`，仍以 `text` 类型投递。
- `at` 可选，字段与原生报文一致；简化体固定产出 `text`，因此 `@` 一定生效。

### 原生报文

```json
{
    "msgtype": "markdown",
    "markdown": { "title": "标题", "text": "### 内容" },
    "at": { "atMobiles": ["150XXXXXXXX"], "atUserIds": ["user123"], "isAtAll": false }
}
```

`msgtype` 只能是 `text`、`link`、`markdown`、`actionCard`、`feedCard`；其他值返回 400 `unsupported_message_type`。

## 消息限制

| 限制            | 规则                                                                                                                                                                      |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 正文长度        | 15000 字节，按消息正文计：`text` 与简化体取 `text`，`markdown` 取 `markdown.text`，`link` 取 `link.text`，`actionCard` 取 `actionCard.text`；超出返回 `message_too_large` |
| `@` 人数        | `atMobiles` 与 `atUserIds` 合计 ≤ 50（`isAtAll` 不计入），超出返回 `at_limit_exceeded`                                                                                    |
| `@` 适用类型    | 仅 `text` 与 `markdown`；`link`/`actionCard`/`feedCard` 携带非空 `at` 返回 `at_not_supported`（`at: {}` 视为没有）                                                        |
| markdown 标题   | `title` 与 `text` 都必填，否则 `invalid_message`                                                                                                                          |
| markdown 的 `@` | 正文必须出现 `@手机号` 才会真正 @ 到人。服务会把 `atMobiles` 中尚未出现在正文里的手机号追加到正文末尾（已出现的不重复追加，幂等）；补齐后的正文参与 15000 字节校验        |
| link            | `title`、`text`、`messageUrl` 必填，`picUrl` 可选                                                                                                                         |
| actionCard      | `title`、`text` 必填；必须有可渲染的跳转方式：`btns`（每个按钮需要 `title` 与 `actionURL`），或成对的 `singleTitle` + `singleURL`                                         |
| feedCard        | 至少一条 `links` 元素                                                                                                                                                     |

字段名大小写不统一是钉钉报文的既有形态：`link` 用 `picUrl` / `messageUrl`，`feedCard` 用 `picURL` / `messageURL`。

## 响应

成功（HTTP 200）：

```json
{ "ok": true, "errmsg": "ok", "errcode": 0, "request_id": "8ba25b9e5dfb4576", "provider": "custom" }
```

失败（HTTP 非 2xx）：

```json
{
    "ok": false,
    "code": "upstream_rejected",
    "errmsg": "sign not match",
    "errcode": 310000,
    "request_id": "8ba25b9e5dfb4576",
    "provider": "custom"
}
```

| 字段            | 类型   | 说明                                                             |
| --------------- | ------ | ---------------------------------------------------------------- |
| `ok`            | bool   | 是否投递成功                                                     |
| `code`          | string | 本地稳定错误码，仅失败时出现；**程序分支只应判断它**             |
| `errmsg`        | string | 成功时为 `ok`；失败时为可读原因（上游失败时通常是上游 `errmsg`） |
| `errcode`       | number | 钉钉数字错误码；本地错误、或上游只给了字符串码时为 `0`           |
| `upstream_code` | string | 可选，钉钉原始字符串错误码（企业通道的业务码即此形态）；无则省略 |
| `request_id`    | string | 与 `X-Request-Id` 响应头、日志一致                               |
| `provider`      | string | 本次请求选定的通道，与是否真的发生投递无关（401/404 也会给出）   |
| `at_dropped`    | bool   | 可选，仅企业通道开启 `ignore_at` 且丢弃了 `@` 时出现             |

上游码分两层：`errcode` 只放数字，`upstream_code` 只放字符串。企业通道的业务码是字符串，因此通常只有 `upstream_code` 有值；当该字符串本身是数字（如 `"90006"`）时两个字段给出同一个码。排障时以 `code` 判断类别、`upstream_code` 定位原因、`errmsg` 读原始描述。

## 错误码

| HTTP | `code`                                  | 含义                                                                                               |
| ---- | --------------------------------------- | -------------------------------------------------------------------------------------------------- |
| 400  | `invalid_json`                          | 请求体为空、不是 JSON 对象，或 JSON 无法解析                                                       |
| 400  | `invalid_request`                       | 请求语义不合法（如 `text` 不是字符串、`at` 不是对象）                                              |
| 400  | `invalid_message`                       | 内容不满足投递要求（markdown 缺标题、正文为空、link 缺 `messageUrl`、actionCard 没有可渲染按钮等） |
| 400  | `message_too_large`                     | 正文超过 15000 字节                                                                                |
| 400  | `unsupported_message_type`              | `msgtype` 不受支持                                                                                 |
| 400  | `unsupported_message_type_for_provider` | 当前通道不支持该类型（企业通道收到 `feedCard`）                                                    |
| 400  | `at_not_supported`                      | `at` 用在了非 `text`/`markdown` 类型上；或企业通道收到 `at` 且未开启 `ignore_at`                   |
| 400  | `at_limit_exceeded`                     | `atMobiles` 与 `atUserIds` 合计超过 50                                                             |
| 400  | `option_not_supported`                  | 展示选项无法被当前通道表达（企业通道的 `hideAvatar="1"`、三按钮以上的横向卡片、超过 5 个按钮）     |
| 401  | `unauthorized`                          | 令牌缺失或不匹配                                                                                   |
| 404  | `not_found`                             | 路径不存在                                                                                         |
| 405  | `method_not_allowed`                    | 路径存在但方法不允许，响应带 `Allow` 头                                                            |
| 413  | `payload_too_large`                     | 请求体超过 `server.max_body_bytes`                                                                 |
| 415  | `unsupported_media_type`                | `Content-Type` 不是 `application/json`                                                             |
| 429  | `rate_limited`                          | 本地或钉钉侧限流；是否有 `Retry-After` 见下                                                        |
| 500  | `internal`                              | 服务内部错误（含 panic 兜底）                                                                      |
| 502  | `upstream_rejected`                     | 钉钉拒绝（签名、关键词、内容审核、凭据无效），或上游 5xx 重试耗尽                                  |
| 504  | `upstream_timeout`                      | 网络/超时类失败且重试预算耗尽（上游未给出错误码）                                                  |

## 响应头

| 头                 | 出现时机                   | 说明                                                                                                                                            |
| ------------------ | -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `X-Request-Id`     | 总是                       | 客户端传入合法的 `X-Request-Id` 时回显，否则生成新的。合法 = 只含 `[A-Za-z0-9._-]` 且长度 ≤ 64；其他值会被丢弃并重新生成                        |
| `Retry-After`      | 仅当本次响应给出等待建议时 | 秒数。`custom` 上游限流为 `600`（对齐 10 分钟封禁），`enterprise` 上游限流为 `1`（QPS 级抖动），本地 `reject` 按令牌桶预约延迟给出（至少 1 秒） |
| `WWW-Authenticate` | 401                        | 固定为 `Bearer realm="dingtalk-robot"`                                                                                                          |
| `Allow`            | 405                        | 该路径允许的方法（如 `POST`）                                                                                                                   |

`wait` 策略下排队耗时超过 `send.timeout` 时同样返回 429，但此时没有等待建议，因此**不带** `Retry-After`——不要把「429 一定有该头」写进客户端逻辑。该头的语义见 [RFC 9110 §10.2.3](https://www.rfc-editor.org/rfc/rfc9110#field.retry-after)。

## 消息样例

以下都是请求体，套用上面的入口即可提交。每条样例都会真实投递一次。

`text`：

```json
{ "msgtype": "text", "text": { "content": "构建完成" }, "at": { "atMobiles": ["150XXXXXXXX"] } }
```

`markdown`（正文里的 `@手机号` 才会真正 @ 到人）：

```json
{
    "msgtype": "markdown",
    "markdown": { "title": "部署完成", "text": "### 生产环境\n\n版本 `v1.4.0` 已上线\n\n@150XXXXXXXX" }
}
```

`link`：

```json
{
    "msgtype": "link",
    "link": {
        "title": "链接标题",
        "text": "链接描述",
        "picUrl": "https://example.com/pic.png",
        "messageUrl": "https://ci.example.com/builds/42"
    }
}
```

`actionCard`（给 `btns`，或给成对的 `singleTitle` + `singleURL`；都不给或只给一半返回 400）：

```json
{
    "msgtype": "actionCard",
    "actionCard": {
        "title": "卡片标题",
        "text": "卡片正文",
        "btns": [
            { "title": "查看日志", "actionURL": "https://ci.example.com/builds/42" },
            { "title": "回滚", "actionURL": "https://ci.example.com/rollback/42" }
        ],
        "btnOrientation": "0"
    }
}
```

`feedCard`（仅 `custom` 通道支持）：

```json
{
    "msgtype": "feedCard",
    "feedCard": {
        "links": [
            {
                "title": "图文标题",
                "picURL": "https://example.com/pic.png",
                "messageURL": "https://ci.example.com/builds/42"
            }
        ]
    }
}
```

企业通道的能力差异（无 `feedCard`、`@` 默认 400、按钮 ≤ 5、不支持 `hideAvatar="1"`、横向排列最多 2 个按钮）见 [投递通道](channels.md#能力差异)。报文字段的官方说明见[自定义机器人发送群消息](https://open.dingtalk.com/document/orgapp/custom-robots-send-group-messages)。

## 客户端建议

- 分支只看 HTTP 状态码与 `code`；`errcode`/`upstream_code` 用于排障，不要拿 `errmsg` 做匹配。
- 投递语义是 **at-least-once**：网络错误与上游 5xx 会按 `send.retries` 退避重试，因此同一条消息可能重复发到群里。业务侧不要假设「一次成功调用只产生一条消息」。
- 429 时若带 `Retry-After`，按该值等待；`custom` 的 600 秒是 10 分钟封禁窗口，提前重试只会继续失败。没有该头时按指数退避加抖动，并检查是否本地 `send.timeout` 已耗尽。
- 502 可能是配置类（如 `310000` 签名不匹配）也可能是瞬时上游故障；后者可重试，前者重试无意义，结合 `errcode`/`upstream_code` 判断。
- 504 是网络/超时耗尽，可按指数退避重试。
- 400、401、413、415 不要重试，先修请求或配置。
- 建议为每次调用生成 `X-Request-Id` 并在告警里带上；日志、响应与指标都可用它串起来。

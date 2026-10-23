# 配置参考

所有配置项都有默认值，按 **环境变量 > 配置文件 > 默认值** 生效。配置在启动时一次性解析并校验，校验不通过进程直接退出，不会带着半套配置继续跑。

## 配置文件

不传 `-config` 时按以下顺序查找，取第一个存在的文件：

```
./config.yaml
./config/config.yaml
/config/config.yaml
```

三个都不存在时只用环境变量，不报错。显式传入的 `-config` 不存在则启动失败。启动日志里的 `source` 字段会给出实际读取的路径；没有该字段表示没有读配置文件。

镜像内已经带了 `/config/config.yaml`（由 `config/example.config.yaml` 复制，值全为空），因此容器里通常总会读到配置文件，环境变量负责覆盖具体值。挂载自己的配置到 `/config/config.yaml` 即可替换。

约定：

- 文件是 YAML；未知键被静默忽略，拼错的键不会报错，改完建议用启动日志或 `/v1/messages` 自检。
- 时长必须写成字符串（`5s`、`200ms`）；写成数字会报 `duration must be a string such as "5s"`。
- 环境变量为空字符串等于「未设置」；配置文件里的空值是真实的空值（例如 `api_token:` 就是空令牌）。
- `PORT` 在 `SERVER_ADDR` 之后应用，因此只要 `PORT` 非空，监听地址就是 `:<PORT>`。
- `SERVER_API_TOKEN` 优先于 `API_TOKEN`，两者都非空时取前者。

## 配置项

共 26 个键。「环境变量」列为 `—` 表示该键只能写在配置文件里，对应的环境变量即使设置也不会被读取。

| 配置键                                     | 环境变量                         | 默认值   | 说明与校验                                                                         |
| ------------------------------------------ | -------------------------------- | -------- | ---------------------------------------------------------------------------------- |
| `provider`                                 | `PROVIDER`                       | `custom` | `custom` 或 `enterprise`；其他值启动失败                                           |
| `server.addr`                              | `SERVER_ADDR` / `PORT`           | `:8080`  | 监听地址；空值启动失败；`PORT` 会覆盖为 `:<port>`                                  |
| `server.api_token`                         | `SERVER_API_TOKEN` / `API_TOKEN` | 空       | 入口令牌；为空且未允许匿名时启动失败                                               |
| `server.allow_unauthenticated`             | `SERVER_ALLOW_UNAUTHENTICATED`   | `false`  | 显式允许匿名访问；置为 `true` 后所有路径都不校验令牌                               |
| `server.max_body_bytes`                    | `SERVER_MAX_BODY_BYTES`          | `65536`  | 请求体上限，必须 > 0；超限返回 413                                                 |
| `server.read_header_timeout`               | `SERVER_READ_HEADER_TIMEOUT`     | `5s`     | 读取请求头的超时，必须 > 0                                                         |
| `server.read_timeout`                      | —                                | `30s`    | 读取整个请求的超时，必须 > 0                                                       |
| `server.write_timeout`                     | —                                | `30s`    | 写响应的超时，必须 > 0                                                             |
| `server.idle_timeout`                      | —                                | `60s`    | keep-alive 空闲超时，必须 > 0                                                      |
| `server.shutdown_timeout`                  | `SERVER_SHUTDOWN_TIMEOUT`        | `15s`    | 优雅停机排空窗口，必须 > 0                                                         |
| `dingtalk.custom.access_token`             | `DINGTALK_ACCESS_TOKEN`          | 空       | `provider=custom` 时必填；Webhook 地址里 `access_token=` 后的值                    |
| `dingtalk.custom.secret`                   | `DINGTALK_SECRET`                | 空       | 加签密钥；留空表示按非加签发送                                                     |
| `dingtalk.enterprise.client_id`            | `DINGTALK_CLIENT_ID`             | 空       | `provider=enterprise` 时必填                                                       |
| `dingtalk.enterprise.client_secret`        | `DINGTALK_CLIENT_SECRET`         | 空       | `provider=enterprise` 时必填                                                       |
| `dingtalk.enterprise.robot_code`           | `DINGTALK_ROBOT_CODE`            | 空       | `provider=enterprise` 时必填                                                       |
| `dingtalk.enterprise.open_conversation_id` | `DINGTALK_OPEN_CONVERSATION_ID`  | 空       | `provider=enterprise` 时必填，`cid` 开头                                           |
| `dingtalk.enterprise.ignore_at`            | `DINGTALK_IGNORE_AT`             | `false`  | 企业通道不支持 `@`；开启后丢弃并标记 `at_dropped`                                  |
| `rate_limit.enabled`                       | `RATE_LIMIT_ENABLED`             | `true`   | 关闭后不创建令牌桶，下面三项也不再校验                                             |
| `rate_limit.per_minute`                    | `RATE_LIMIT_PER_MINUTE`          | `18`     | 每分钟条数，允许小数（如 `0.5`）；必须 > 0，且 `custom` ≤ 20、`enterprise` ≤ 1500  |
| `rate_limit.burst`                         | `RATE_LIMIT_BURST`               | `3`      | 令牌桶容量，必须 ≥ 1                                                               |
| `rate_limit.strategy`                      | `RATE_LIMIT_STRATEGY`            | `wait`   | `wait` 排队压平突发；`reject` 立即返回 429                                         |
| `send.timeout`                             | `SEND_TIMEOUT`                   | `10s`    | 单次请求总预算，覆盖限流排队、重试与投递，必须 > 0；同时作为上游 HTTP 客户端的超时 |
| `send.retries`                             | `SEND_RETRIES`                   | `2`      | 仅对可重试错误生效，取值 0–5                                                       |
| `send.backoff_base`                        | `SEND_BACKOFF_BASE`              | `200ms`  | 退避基数，第 n 次重试等待 `base × 2^(n-1)`，必须 ≥ 0                               |
| `log.level`                                | `LOG_LEVEL`                      | `info`   | `debug` / `info` / `warn` / `error`，其他值启动失败                                |
| `log.format`                               | `LOG_FORMAT`                     | `text`   | `text`（单行 key=value）或 `json`，其他值启动失败                                  |

带注释的完整示例见 [`config/example.config.yaml`](../config/example.config.yaml)。

## 校验规则

启动时收集**全部**问题后一次报出，格式为：

```
fatal: invalid configuration:
  - dingtalk.custom.access_token is required when provider is "custom"
  - server.api_token is required unless server.allow_unauthenticated is true
```

环境变量格式错误也进入同一份列表，例子：

```
environment variable SEND_TIMEOUT="10" is not a duration such as "10s"
environment variable RATE_LIMIT_PER_MINUTE="abc" is not a number
environment variable LOG_LEVEL="verbose" ...（级别由 log.level 的校验给出）
```

规则汇总：

- `provider=custom` 必须有 `dingtalk.custom.access_token`。
- `provider=enterprise` 必须同时有 `client_id`、`client_secret`、`robot_code`、`open_conversation_id`；缺失项会在同一条错误里全部列出。
- `server.api_token` 为空时必须显式 `server.allow_unauthenticated: true`。
- `server.addr` 非空；`server.max_body_bytes` > 0；六个时长（`read_header_timeout`、`read_timeout`、`write_timeout`、`idle_timeout`、`shutdown_timeout`、`send.timeout`）都 > 0。
- `rate_limit` 仅在 `enabled=true` 时校验：`per_minute` > 0 且不超过通道上限，`burst` ≥ 1，`strategy` 只能是 `wait`/`reject`。
- `send.retries` 在 0–5 之间；`send.backoff_base` ≥ 0。
- `log.level` 与 `log.format` 取值受限。

`rate_limit.per_minute` 的上限按通道取值，依据来自官方口径：

| 通道         | 上限 | 依据                                                                     |
| ------------ | ---- | ------------------------------------------------------------------------ |
| `custom`     | 20   | 每个机器人每分钟 20 条，超限封禁 10 分钟                                 |
| `enterprise` | 1500 | 服务端 API 每应用每接口每分钟 1500 次（另有 QPS 维度，实际配额可能更低） |

默认 18 是给 `custom` 留出的余量；`enterprise` 可以调到 1500，但组织权益可能先于这个数字生效，往上调时先用小流量验证。

## 凭据处理

- 只通过环境变量、Secret 或只读挂载注入；不要把真实凭据写进镜像、镜像 tag 或仓库。
- `debug` 级别会打印一份生效配置，其中 `server.api_token`、`dingtalk.custom.access_token`、`dingtalk.custom.secret`、`dingtalk.enterprise.client_secret` 被替换为 `***`；`client_id`、`robot_code`、`open_conversation_id` 不脱敏。
- `-healthcheck` 与主进程共用同一份解析结果，因此监听地址只写在配置文件里时，探活也会跟随；配置读不到时回落到 `:8080`。详见 [部署](deploy.md#探针)。

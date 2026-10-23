# 观测与排障

服务把日志写到 stdout，把指标暴露在 `/metrics`。两者都用 `request_id` 关联，排障时先拿到它。

## 日志

`log.format` 为 `text`（默认，单行 key=value）或 `json`；`log.level` 控制级别。时间戳是秒级 RFC3339（带时区），级别为小写。

| 事件     | 级别  | 关键字段                                                                     |
| -------- | ----- | ---------------------------------------------------------------------------- |
| 启动     | info  | `msg=starting`、`version`、`source`（读到的配置文件；没有则不出现）          |
| 生效配置 | debug | `msg="configuration loaded"`、`config`（凭据已脱敏）                         |
| 监听     | info  | `msg=listening`、`addr`、`provider`                                          |
| 业务请求 | info  | `msg="request handled"`、12 个访问字段                                       |
| 5xx 请求 | error | `msg="request failed"`，字段同上                                             |
| 探针请求 | debug | `msg="request handled"`，字段同上                                            |
| 重试     | debug | `msg="retrying delivery"`、`provider`、`attempt`、`backoff`、`errcode`       |
| panic    | error | `msg="panic while handling request"`、`request_id`、`panic`                  |
| 停机     | info  | `msg="graceful shutdown started"`、`reason`、`timeout`；结束时 `msg=stopped` |

访问日志字段（共 12 个，空值不输出）：

| 字段          | 含义                                                       |
| ------------- | ---------------------------------------------------------- |
| `request_id`  | 与响应头 `X-Request-Id` 一致；由客户端传入或服务生成       |
| `method`      | HTTP 方法                                                  |
| `path`        | 原始请求路径                                               |
| `route`       | 命中的路由模式（`/v1/messages` 等）；未命中为 `unmatched`  |
| `status`      | 响应状态码                                                 |
| `msgtype`     | 消息类型（解析成功后才有）                                 |
| `provider`    | 当前通道（发生投递时才有）                                 |
| `errcode`     | 上游数字错误码（非 0 时才有）                              |
| `attempts`    | 投递尝试次数（发生投递时才有）                             |
| `bytes`       | 响应体字节数                                               |
| `duration_ms` | 处理耗时（毫秒）                                           |
| `remote_ip`   | 来源 IP；取 `X-Forwarded-For` 的第一个值，没有则取连接地址 |

示例：

```text
time=2026-10-23T15:04:05+08:00 level=info msg="request handled" request_id=8ba25b9e5dfb4576 method=POST path=/v1/messages route=/v1/messages status=200 msgtype=text provider=custom attempts=1 bytes=98 duration_ms=42 remote_ip=10.0.16.21
time=2026-10-23T15:04:07+08:00 level=error msg="request failed" request_id=3f0c1d2e4a5b6c7d method=POST path=/v1/messages route=/v1/messages status=502 msgtype=markdown provider=custom errcode=310000 attempts=1 bytes=142 duration_ms=88 remote_ip=10.0.16.21
```

约定与边界：

- 不记录请求体与响应体；`errmsg` 只出现在 HTTP 响应里。
- `debug` 级别会打印生效配置，凭据字段已掩码；仍然不要长期开 debug。
- `remote_ip` 来自 `X-Forwarded-For` 的第一个值，服务不做校验；该字段只能作为线索，不能作为审计证据。
- 同一 `request_id` 的多行日志（重试、panic）可以直接串联。

## 指标

`GET /metrics` 输出 Prometheus 文本格式，需要 `Authorization: Bearer <token>`；同时包含 Go 运行时与进程指标。自定义指标统一使用 `dingtalk_robot_` 命名空间：

| 指标                                       | 类型      | 标签                | 含义                                                                                 |
| ------------------------------------------ | --------- | ------------------- | ------------------------------------------------------------------------------------ |
| `dingtalk_robot_requests_total`            | counter   | `route`、`status`   | 入口请求数；`status` 是三位数字，401 与 `route="unmatched"` 都计入                   |
| `dingtalk_robot_deliveries_total`          | counter   | `provider`、`class` | 每次投递尝试的结果分类：`ok`/`throttled`/`retryable`/`auth`/`config`/`rejected`      |
| `dingtalk_robot_delivery_duration_seconds` | histogram | `provider`          | 单次投递尝试耗时；桶为 5ms、10ms、25ms、50ms、100ms、250ms、500ms、1s、2.5s、5s、10s |
| `dingtalk_robot_inflight_requests`         | gauge     | —                   | 正在处理的请求数                                                                     |
| `dingtalk_robot_ratelimit_events_total`    | counter   | `action`            | 本地限流事件：`rejected`（reject 策略直接拒绝）或 `cancelled`（wait 排队超时）       |

常用查询：

```promql
# 入口 5xx 比例
sum(rate(dingtalk_robot_requests_total{status=~"5.."}[5m]))
  / sum(rate(dingtalk_robot_requests_total[5m]))

# 401 速率：令牌配错或调用方漏配
sum(rate(dingtalk_robot_requests_total{status="401"}[5m]))

# 上游限流占比
sum(rate(dingtalk_robot_deliveries_total{class="throttled"}[5m]))
  / sum(rate(dingtalk_robot_deliveries_total[5m]))

# 投递耗时 p95（按通道）
histogram_quantile(0.95,
  sum by (le, provider) (rate(dingtalk_robot_delivery_duration_seconds_bucket[5m])))

# 未命中路由：客户端路径写错
sum(rate(dingtalk_robot_requests_total{route="unmatched"}[5m]))

# 在途请求
dingtalk_robot_inflight_requests
```

告警建议（阈值按实际流量调整，先观察一周再收紧）：

| 告警         | 表达式（起点）                      | 说明                                      |
| ------------ | ----------------------------------- | ----------------------------------------- |
| 5xx 比例偏高 | 5xx 比例 > 1% 持续 10 分钟          | 区分 502（上游拒绝/配置）与 504（网络）   |
| 令牌异常     | `status="401"` 速率 > 0 持续 5 分钟 | 通常是部署时令牌不一致，不必等用户报障    |
| 上游限流     | throttled 占比 > 5% 持续 10 分钟    | `custom` 会进入 10 分钟封禁，越早发现越好 |
| 投递变慢     | p95 > `send.timeout` 的 50%         | 排队开始吃掉预算，请求更容易以 429 结束   |
| 堆积         | `inflight_requests` 持续升高        | 上游变慢或限流排队导致                    |

指标抓取配置里要给 `/metrics` 带令牌，例如：

```yaml
scrape_configs:
    - job_name: dingtalk-robot
      authorization:
          credentials: <API_TOKEN>
      static_configs:
          - targets: ["dingtalk-robot:8080"]
```

## 容量与限流

一次请求的时间预算由 `send.timeout` 统一覆盖，顺序是「排队等令牌 → 投递（含重试与退避）」。因此：

- `rate_limit.per_minute` 调得很低时，排队会占用更多预算，需要同步调大 `send.timeout`，否则请求会在真正投递前就以 429 结束（且没有 `Retry-After`）。
- `wait` 策略把突发压平，客户端看到的是延迟而不是 429；`reject` 策略立即返回 429 并给出预约延迟，适合宁可失败也不要排队的场景。
- `send.retries` 只作用于可重试类错误（网络、上游 5xx、`-1`）；`enterprise` 的令牌失效重试是通道内部行为，不占用这个次数。
- `custom` 的官方硬限制是每机器人 20 条/分钟；默认 18 留出余量。`enterprise` 的分钟级上限是 1500，但组织权益可能更低。
- 多副本部署时总速率会叠加，按 `每个副本 = 目标总量 / 副本数` 配置，详见 [部署](deploy.md#多副本)。

## 排障决策树

```text
请求失败
  |
  +-- 401 --> 令牌缺失/不匹配：核对 API_TOKEN 与调用方；探针不需要令牌
  |           看 dingtalk_robot_requests_total{status="401"}
  |
  +-- 404/405 --> 路径或方法错：发送只有 POST /v1/messages；405 带 Allow 头
  |
  +-- 413/415 --> 请求体超过 max_body_bytes / Content-Type 不是 application/json
  |
  +-- 400 --> 按 code 修请求：
  |           invalid_json / invalid_request / invalid_message /
  |           message_too_large / unsupported_message_type /
  |           at_not_supported / at_limit_exceeded / option_not_supported
  |
  +-- 429 --> 先看有没有 Retry-After
  |           +-- 600        custom 上游封禁 10 分钟，等封禁结束
  |           +-- 1          enterprise QPS 级抖动，稍后重试
  |           +-- 其他数字   本地 reject 策略的预约延迟
  |           +-- 没有        本地 wait 排队耗尽 send.timeout
  |
  +-- 502 --> 看 errcode / upstream_code
  |           +-- 310000 / 400101 / 400102 / 400106  custom 配置类
  |           +-- QpsLimit / Throttling / 9000x         enterprise 限流
  |           +-- InvalidAuthentication                enterprise 凭据/授权
  |           +-- 其他 / 无码                           上游拒绝或 5xx 重试耗尽
  |
  +-- 504 --> 网络、DNS 或上游不可达；检查上游代理与防火墙
  |
  +-- 500 --> 内部错误或 panic；按 request_id 搜 error 日志
```

几个容易误判的点：

- 429 不等于「一定会恢复」：`custom` 的 600 秒是封禁窗口，提前重试只会延长失败时间。
- 502 既可能是重试无意义的配置错误，也可能是瞬时上游故障；不要只按状态码决定是否重试。
- `provider` 字段在所有响应里都有，包括 401/404；它表示选定的通道，不表示发生过投递。
- `/healthz` 只证明进程活着，不证明钉钉可达；上游故障时它仍然是 200。

## 安全边界

- 令牌只通过环境变量、Secret 或只读文件注入；服务只接受 `Authorization: Bearer`，不接受查询串令牌。
- 令牌比较使用常量时间比较；`401` 响应不会回显令牌。
- 配置转储（debug）会掩码 `api_token`、`custom.access_token`、`custom.secret`、`enterprise.client_secret`；`client_id`、`robot_code`、`open_conversation_id` 不掩码。
- `X-Forwarded-For` 被无条件信任，日志中的 `remote_ip` 可被伪造；审计用途不要依赖它。
- 不要把 8080 直接暴露到公网，TLS 由反向代理终结；`disabletls=yes` 只用于可信内网。
- 已知风险：`custom` 通道在建立连接失败时，上游错误的原始文本可能包含完整 webhook 地址（含 `access_token` 与 `sign`），并作为 `errmsg` 返回给调用方。不要把 `errmsg` 原样透传给不受信任的终端，也不要把响应体写入共享日志。

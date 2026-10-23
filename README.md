# 钉钉机器人转发服务

把一次 HTTP POST 转发到钉钉群，给 Watchtower、CI、监控系统这类只会发 Webhook 的工具用。

- 一个二进制、一个容器，无数据库、无外部依赖
- 两条投递通道（自定义机器人 / 企业内部应用机器人），同一套入参
- 入口 Bearer 鉴权、请求体限额、出站限流、按错误分类重试、结构化日志、Prometheus 指标、优雅停机

## 快速开始

生成入口令牌并记下来——容器重建时要复用同一个值：

```bash
export API_TOKEN="$(openssl rand -hex 16)"
```

启动（示例只把端口发布到本机回环地址，容器内仍监听 `:8080`）：

```bash
docker run -d --name dingtalk-robot --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e API_TOKEN="$API_TOKEN" \
  -e DINGTALK_ACCESS_TOKEN="<自定义机器人 access_token>" \
  -e DINGTALK_SECRET="<加签 secret，未加签留空>" \
  jerryin/dingtalk-robot
```

发一条消息：

```bash
curl -X POST http://127.0.0.1:8080/v1/messages \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"hello"}'
```

```json
{ "ok": true, "errmsg": "ok", "errcode": 0, "request_id": "8ba25b9e5dfb4576", "provider": "custom" }
```

`API_TOKEN` 必填，缺失时进程直接退出；确实需要匿名访问时设 `SERVER_ALLOW_UNAUTHENTICATED=true`。失败先从[排障决策树](https://github.com/clouin/dingtalk-robot/blob/master/docs/operations.md#排障决策树)入手。

## 选择通道

|              | `custom`                                    | `enterprise`                                                               |
| ------------ | ------------------------------------------- | -------------------------------------------------------------------------- |
| 凭据         | webhook `access_token`（可选加签 `secret`） | 企业应用 `client_id`/`client_secret`、`robot_code`、`open_conversation_id` |
| 目标群       | webhook 所属的那一个群                      | `open_conversation_id` 指向的那一个群                                      |
| 官方限速口径 | 每机器人 20 条/分钟，超限封禁 10 分钟       | 服务端 API 频率限制（每应用每接口 40 次/秒、1500 次/分）                   |

默认 `custom`：一条 webhook 就够。需要企业凭据体系、或无法在群里放 webhook 时选 `enterprise`；完整能力差异与取 `open_conversation_id` 的方法见[投递通道](https://github.com/clouin/dingtalk-robot/blob/master/docs/channels.md)。

## 接口

| 方法   | 路径           | 说明                         |
| ------ | -------------- | ---------------------------- |
| `POST` | `/v1/messages` | 发送消息                     |
| `GET`  | `/healthz`     | 存活探针，不依赖上游         |
| `GET`  | `/readyz`      | 就绪探针，停机排空时返回 503 |
| `GET`  | `/metrics`     | Prometheus 指标              |

`/healthz`、`/readyz` 不需要令牌；其余路径都要 `Authorization: Bearer <token>`。完整契约、错误码与响应头见[接口契约](https://github.com/clouin/dingtalk-robot/blob/master/docs/api.md)。

## 文档

| 文档                                                                                                    | 内容                                 |
| ------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| [配置](https://github.com/clouin/dingtalk-robot/blob/master/docs/configuration.md)                      | 26 个配置键、优先级、启动期校验      |
| [接口契约](https://github.com/clouin/dingtalk-robot/blob/master/docs/api.md)                            | 请求/响应、错误码、客户端重试建议    |
| [投递通道](https://github.com/clouin/dingtalk-robot/blob/master/docs/channels.md)                       | 选型、凭据、能力差异、上游失败特征   |
| [部署](https://github.com/clouin/dingtalk-robot/blob/master/docs/deploy.md)                             | 镜像形态、Compose、K8s、多副本、回滚 |
| [观测与排障](https://github.com/clouin/dingtalk-robot/blob/master/docs/operations.md)                   | 日志字段、指标、告警、决策树         |
| [开发](https://github.com/clouin/dingtalk-robot/blob/master/docs/development.md)                        | 测试、构建、CI、扩展点               |
| [Watchtower 接入](https://github.com/clouin/dingtalk-robot/blob/master/docs/integrations/watchtower.md) | 更新通知转发示例                     |

## 部署要点

- 镜像以非 root（UID 65532）运行，内置 `HEALTHCHECK`（进程自身的 `-healthcheck` 子命令），默认时区 `Asia/Shanghai`。
- 镜像自带 `/config/config.yaml`（示例配置的空值版本），用环境变量或挂载覆盖；配置只读挂载即可。
- Kubernetes 探针可直接用 `/healthz` 与 `/readyz`；滚动更新、多副本与限流的注意事项见[部署](https://github.com/clouin/dingtalk-robot/blob/master/docs/deploy.md)。

## 安全须知

- 令牌只通过环境变量或 Secret 注入，不要写进镜像、镜像 tag 或提交到仓库。
- 对外必须经过带 HTTPS 的反向代理，不要直接把 8080 开到公网；`disabletls=yes` 只适用于可信内网。
- 服务只接受 `Authorization` 头，不接受查询串令牌；配置转储中的凭据已做掩码。

## 版本与许可

`-version` 打印构建版本。许可证：[MIT](LICENSE)。

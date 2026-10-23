# Watchtower 通知接入

把 [Watchtower](https://containrrr.dev/watchtower/) 的更新通知转发到钉钉群：本服务作为 [shoutrrr](https://containrrr.dev/shoutrrr/) 的 generic webhook 目标，两者同处一个 Compose 网络，无需对外暴露端口。

其余部署方式见 [部署](../deploy.md)；凭据键名见 [配置](../configuration.md#配置项) 与 [`config/example.config.yaml`](../../config/example.config.yaml)。

## Compose

```yaml
services:
    dingtalk-robot:
        image: jerryin/dingtalk-robot
        restart: always
        expose:
            - "8080"
        environment:
            - API_TOKEN=${API_TOKEN:?required}
            - DINGTALK_ACCESS_TOKEN=${DINGTALK_ACCESS_TOKEN:?required}
            - DINGTALK_SECRET=${DINGTALK_SECRET:-}

    watchtower:
        depends_on:
            dingtalk-robot:
                condition: service_healthy
        image: containrrr/watchtower
        restart: always
        volumes:
            - /var/run/docker.sock:/var/run/docker.sock
        environment:
            TZ: "Asia/Shanghai"
            WATCHTOWER_NOTIFICATIONS: shoutrrr
            # @authorization 是 shoutrrr 的自定义头语法（@key=value，驼峰自动转为连字符头名）。
            # 值里的空格必须写成 %20，否则 "Bearer" 与 token 会被拆成两个参数。
            WATCHTOWER_NOTIFICATION_URL: "generic://dingtalk-robot:8080/v1/messages?template=json&messagekey=text&disabletls=yes&@authorization=Bearer%20${API_TOKEN:?required}"
            WATCHTOWER_NOTIFICATION_TEMPLATE: '{{range .}}{{.Time.Format "2006-01-02 15:04:05"}} ({{.Level}}): {{.Message}}{{println}}{{end}}'
            DOCKER_API_VERSION: 1.52
        command: --cleanup --schedule "0 0 4 * * *"
```

## 关于那条 URL

- **令牌必须带。** `/v1/messages` 只认 `Authorization: Bearer <token>`，不带就是 401。shoutrrr 用 `@` 前缀查询变量加请求头（官方示例 `@acceptLanguage=tlh-Piqd` → `Accept-Language: tlh-Piqd`），所以写 `@authorization=Bearer%20<token>`。
- **凭据全部外部化。** `API_TOKEN`、`DINGTALK_ACCESS_TOKEN`、`DINGTALK_SECRET` 都从宿主机环境变量或同目录 `.env` 读取，不要把真实值写进 `docker-compose.yml`。`${VAR:?required}` 在变量缺失时让 compose 直接报错，不会等到运行期才 401/400。
- **令牌只维护一处。** `${API_TOKEN:?required}` 同时注入容器与 watchtower 的通知 URL，改一处即可。
- `disabletls=yes` 是因为容器之间走明文 HTTP；只适用于可信内网。要对外暴露请加反向代理并上 HTTPS，见 [部署](../deploy.md#反向代理与-tls)。
- `messagekey=text` 让 shoutrrr 生成 `{"text":"..."}`，正好对应本服务的简化载荷；`template=json` 默认带 `Content-Type: application/json`。
- 如果所用 Watchtower 内置的 shoutrrr 版本不支持 `@` 参数，就无法在 URL 里带头；此时改用前置反向代理注入 `Authorization`。

## 自检

通知没到群里时，按顺序排除：

1. `docker compose ps` 确认 `dingtalk-robot` 为 healthy（`depends_on: service_healthy` 不满足时 watchtower 不会启动）。镜像的健康检查调用的是 `/healthz`，语义见 [部署](../deploy.md#探针)。
2. `docker compose logs dingtalk-robot` 看是否有 `status=401`——多为 `API_TOKEN` 在容器与 URL 两处不一致。
3. 手工触发一次投递，区分是 shoutrrr 侧还是投递侧的问题；命令与期望响应见 [开发](../development.md#冒烟测试)，失败分支见 [排障决策树](../operations.md#排障决策树)。

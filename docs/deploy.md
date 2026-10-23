# 部署

服务是一个无状态单进程容器：没有数据库、没有本地状态、没有定时任务。所有可变配置都通过环境变量或配置文件注入，所有输出都走 stdout。

## 镜像形态

| 项目       | 内容                                                                                                        |
| ---------- | ----------------------------------------------------------------------------------------------------------- |
| 基础镜像   | `alpine:3.24.2`（按 digest 固定），静态链接的 Go 二进制（`CGO_ENABLED=0`）                                  |
| 运行用户   | `65532:65532`（nonroot，无登录 shell）                                                                      |
| 时区       | `Asia/Shanghai`（镜像内写入 `/etc/localtime`，日志时间戳带 +08:00）                                         |
| 可执行文件 | `/usr/bin/dingtalk-robot`，入口为 `ENTRYPOINT`，参数追加在后面                                              |
| 自带配置   | `/config/config.yaml` 由 `config/example.config.yaml` 复制，值全为空；声明了 `VOLUME /config`               |
| 默认监听   | `SERVER_ADDR=:8080`，`EXPOSE 8080/tcp`                                                                      |
| 健康检查   | `HEALTHCHECK --interval=15s --timeout=5s --retries=3 --start-period=5s`，执行 `dingtalk-robot -healthcheck` |
| 多架构     | CI 发布 `linux/amd64`、`linux/arm64`、`linux/ppc64le`、`linux/s390x`、`linux/arm/v7`                        |
| 版本       | 构建时用 `-ldflags "-X main.version=<tag>"` 注入，`-version` 可打印                                         |

进程不写任何文件，因此可以只读挂载配置、启用只读根文件系统。

## docker run

```bash
docker run -d --name dingtalk-robot --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e API_TOKEN="$API_TOKEN" \
  -e DINGTALK_ACCESS_TOKEN="<access_token>" \
  -e DINGTALK_SECRET="<secret 或留空>" \
  jerryin/dingtalk-robot
```

只把端口发布到 `127.0.0.1`，对外由反向代理终结 TLS。容器内仍监听 `:8080`。

## Compose

```yaml
services:
    dingtalk-robot:
        image: jerryin/dingtalk-robot
        restart: always
        ports:
            - "127.0.0.1:8080:8080"
        environment:
            - API_TOKEN=${API_TOKEN:?required}
            - DINGTALK_ACCESS_TOKEN=${DINGTALK_ACCESS_TOKEN:?required}
            - DINGTALK_SECRET=${DINGTALK_SECRET:-}
        # 需要配置文件时：
        # volumes:
        #     - ./config.yaml:/config/config.yaml:ro
```

`${VAR:?required}` 在变量缺失时让 Compose 直接报错，而不是等到运行期 401/400。

## 配置注入

两种方式可以混用，环境变量优先：

- **环境变量**：适合容器编排与 Secret 注入。镜像自带的 `/config/config.yaml` 是全空值的示例，环境变量覆盖具体项。
- **挂载配置文件**：把文件挂到 `/config/config.yaml`。如果挂载的是**目录**，会遮住镜像自带的那份，此时目录里必须有 `config.yaml`；只给环境变量时也可以挂空目录，服务会回落到「仅环境变量」。
- 文件必须对 UID 65532 可读。只读挂载（`:ro`）即可，进程不会改写它。

## Kubernetes

```yaml
containers:
    - name: dingtalk-robot
      image: jerryin/dingtalk-robot
      env:
          - name: API_TOKEN
            valueFrom: { secretKeyRef: { name: dingtalk-robot, key: api-token } }
          - name: DINGTALK_ACCESS_TOKEN
            valueFrom: { secretKeyRef: { name: dingtalk-robot, key: access-token } }
      ports:
          - containerPort: 8080
      livenessProbe:
          httpGet: { path: /healthz, port: 8080 }
          initialDelaySeconds: 5
          periodSeconds: 10
      readinessProbe:
          httpGet: { path: /readyz, port: 8080 }
          periodSeconds: 10
      terminationGracePeriodSeconds: 20 # 大于 server.shutdown_timeout（默认 15s）
```

`preStop` 不是必需，但推荐加一个短 sleep（例如 `sleep 5`）：服务收到 SIGTERM 会立刻把 `/readyz` 置为 503 并开始排空，而 Service/Ingress 摘除端点有延迟；这段延迟内的新请求如果没有 preStop 兜住，会打到正在退出的 Pod。

探针语义与 `-healthcheck` 的行为见下节。Kubernetes 探针本身的配置说明见[官方文档](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)。

## 探针

| 端点           | 返回                                     | 用途                             |
| -------------- | ---------------------------------------- | -------------------------------- |
| `GET /healthz` | 200 `text/plain`，正文 `ok`              | 存活探针，不依赖上游，也不读配置 |
| `GET /readyz`  | 200 `ready`；停机排空期间 503 `draining` | 就绪探针，负载均衡据此停止转发   |
| `GET /metrics` | Prometheus 文本                          | 指标抓取，**需要令牌**           |

- `/healthz`、`/readyz` 免鉴权；其他路径（含 `/metrics`）都要 `Authorization: Bearer <token>`。
- 进程收到 SIGTERM/SIGINT 后先把就绪置为 false，再在 `server.shutdown_timeout` 内排空在途请求；超时未排空会以非零状态退出。
- `-healthcheck` 子命令请求 `http://<监听地址>/healthz`，2 秒超时；监听地址取自同一份配置（配置文件里的 `server.addr` 也会被跟随），通配地址回退到 `127.0.0.1`。成功退出 0，失败打印 `healthcheck failed: ...` 并退出 1。
- 镜像已内置 `HEALTHCHECK`，Docker 与 Compose 下无需额外配置。

## 多副本

- 限流是**进程内**令牌桶，多个副本的实际总量是各副本之和。要把总速率控制在 N 条/分钟，每个副本设 `rate_limit.per_minute = N / 副本数`。
- `custom` 的 20 条/分钟是机器人维度的硬限制，多副本也不会放宽；超过仍然封禁 10 分钟。
- 服务无共享状态，任意副本都能处理任意请求；请求之间没有顺序依赖。
- 同一个 `request_id` 只存在于处理它的那个副本的日志里，跨副本排障时要逐个 Pod 搜。

## 反向代理与 TLS

服务本身只监听明文 HTTP，TLS 必须由反向代理终结：

- 不要直接把 8080 开到公网；示例统一绑定 `127.0.0.1` 或只在集群内 `expose`。
- 代理要原样转发 `Authorization` 与 `X-Request-Id`，并按需注入 `X-Forwarded-For`。
- 服务**无条件信任** `X-Forwarded-For` 的第一个值作为日志里的 `remote_ip`；代理必须覆盖而不是追加客户端自带的该头，否则日志里的来源可被伪造。
- 配置了 `disabletls=yes`（如 Watchtower 的容器内调用）只适用于可信内网。

## 升级与回滚

- 镜像 tag 由 CI 在 `v*` 标签上按语义化版本生成，同时发布多架构；生产环境建议固定 digest。
- 回滚到旧版本前注意：`enterprise` 通道的 `rate_limit.per_minute` 现在允许超过 20（上限 1500），旧版本会以配置校验失败拒绝启动，回滚前先把它调回 ≤ 20。
- 响应中的 `upstream_code` 是新增字段；旧版本会忽略它，并把 `errcode` 在企业通道上恢复为恒 0。客户端应始终按 `code` 分支。
- 滚动更新时依赖 `/readyz` 摘流与 `terminationGracePeriodSeconds`，不要用 `Recreate` 造成不必要的断流。

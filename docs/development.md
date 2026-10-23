# 开发

面向改代码的人。只想部署使用，看 [README](../README.md)。

## 前置条件

- Go 1.27.3（与 `go.mod` 的 `go` 指令一致，用 `go version` 校验）。安装包见 [Go 下载页](https://go.dev/dl/)。
- Docker（仅在验证镜像时需要）。

## 目录

```text
cmd/dingtalk-robot/   入口：装配、信号、-config/-healthcheck/-version
internal/             业务实现（包职责见包注释）
pkg/dingtalk/         钉钉报文结构（对外契约）
tools/getcid/         离线辅助工具，独立 module，不参与镜像构建
config/               示例配置
docs/                 用户与维护者文档
```

包的职责以包注释与代码为准；不在此维护重复的结构说明。

## 本地启动

启动需要两样东西：投递凭据 + 入口令牌。缺失时进程会一次列出全部问题并退出。

环境变量（最快）：

```bash
API_TOKEN=dev-token \
DINGTALK_ACCESS_TOKEN="<webhook token>" \
DINGTALK_SECRET="<加签 secret，非加签可省略>" \
LOG_LEVEL=debug \
go run ./cmd/dingtalk-robot
```

配置文件：

```bash
cp config/example.config.yaml config/config.yaml
# 填写 server.api_token、dingtalk.custom.access_token、dingtalk.custom.secret
go run ./cmd/dingtalk-robot
```

`config/config.yaml` 已在 `.gitignore` 中。企业通道把上面的凭据换成 `PROVIDER=enterprise` 与四项企业配置即可，字段见 [配置](configuration.md#配置项)。

启动成功会打印 `msg=listening addr=... provider=...`；`source` 字段表示读到的配置文件，没有该字段表示只用环境变量。

## 冒烟测试

> 下面两条投递会真的往群里发消息，需要有效凭据。单元测试用 `httptest` 拦截上游，不需要真实凭据。

```bash
curl -s localhost:8080/healthz              # ok
curl -s localhost:8080/readyz               # ready

# 不带令牌应返回 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/v1/messages \
  -H 'Content-Type: application/json' -d '{"text":"hello"}'

# 正常投递
curl -s -X POST localhost:8080/v1/messages \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"text":"hello"}'

# 路由层错误也是 JSON：404 / 405
curl -s -o /dev/null -w '%{http_code} %{content_type}\n' -H 'Authorization: Bearer dev-token' localhost:8080/nope
curl -s -o /dev/null -w '%{http_code} %{content_type}\n' -H 'Authorization: Bearer dev-token' localhost:8080/v1/messages

# 指标（含运行时与进程指标）
curl -s -H 'Authorization: Bearer dev-token' localhost:8080/metrics | grep -E '^(dingtalk_robot_|go_goroutines|process_cpu)'
```

## 测试与静态检查

```bash
go test ./...                 # 单元测试
go test -race ./...           # 提交前必须过
go vet ./...
gofmt -l .                    # 应无输出
golangci-lint run ./...       # 配置见 .golangci.yml，应为 0 issues
govulncheck ./...             # 依赖漏洞
```

测试分层：

- `internal/provider/*`：用 `httptest` 顶替上游，覆盖加签、状态码/错误码分类、重试分类、企业令牌缓存与 401 重试。
- `internal/httpapi`：路由、中间件、鉴权、错误码与响应头。
- `internal/relay`：校验顺序、限流策略、退避次数、结果映射。
- `internal/e2e`：真实装配 router → relay → provider，只替换最外层上游，验证两条通道的完整链路。

## 构建

```bash
go build ./cmd/dingtalk-robot        # 产物在当前目录
./dingtalk-robot -version
./dingtalk-robot -healthcheck        # 探活，退出码表达结果
```

`go build` 的产物已在 `.gitignore` 中，不会误提交。

```bash
docker build -t dingtalk-robot .
docker run --rm -p 8080:8080 \
  -e API_TOKEN=dev-token -e DINGTALK_ACCESS_TOKEN=xxx \
  dingtalk-robot
docker exec "<container>" /usr/bin/dingtalk-robot -healthcheck
```

构建阶段需要拉基础镜像与 Go 模块；网络受限时给 `docker build` 注入 `http_proxy`/`https_proxy`，不要把这些值写进 Dockerfile。镜像以非 root 用户运行，配置只读挂载即可。

## CI 与发布

工作流：`.github/workflows/docker-ci.yml`，在推送 `v*` 标签或手动触发时运行。

```text
test  ──> docker
  |          |
  |          +-- Buildx 多架构构建：amd64 / arm64 / ppc64le / s390x / arm/v7
  |          +-- 推送 <Docker Hub 仓库>（PR 不推送）
  |          +-- 用 -build-arg VERSION=<tag> 注入版本
  |          +-- 用 README.md 更新 Docker Hub 描述
  |
  +-- go build ./...
  +-- go vet ./...
  +-- go test -race ./...
  +-- golangci-lint（latest）
  +-- govulncheck（latest）
```

需要仓库 Secret：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`。镜像仓库名在 workflow 的 `REPOSITORY` 环境变量里。版本号默认 `2.0.0`，发布时由 `VERSION` 覆盖。

## 扩展点

新增一种消息类型：

1. `pkg/dingtalk/`：补报文结构与 `msgtype` 常量（字段名以官方报文为准）。
2. `internal/message/`：加 `Type` 常量与 `SupportedTypes()`，在 `Validate` 里补必填项与长度口径。
3. `internal/ingest/`：加解析分支；决定该类型是否允许 `at`（不允许时走 `rejectAt`）。
4. `internal/provider/custom/`：在 `Payload` 中序列化。
5. `internal/provider/enterprise/`：在 `buildMessage` 中映射模板；无法表达的能力在 `Validate` 里显式拒绝，不要静默忽略。
6. 文档：`docs/api.md` 加样例，`docs/channels.md` 更新能力矩阵。
7. 测试：ingest、message、两个 provider、e2e 各补一例。

新增一条投递通道：

1. 新建 `internal/provider/<name>/`，实现 `Name`/`Validate`/`Send`；`Send` 只做一次尝试，用 `provider.NewHTTPClient` 复用连接。
2. 把上游错误归入 `ClassOK`/`ClassThrottled`/`ClassRetryable`/`ClassAuth`/`ClassConfig`/`ClassRejected` 之一；数字码填 `ErrCode`，字符串码填 `UpstreamCode`，限流给出 `RetryAfter`。
3. `internal/config/`：加配置结构、环境变量与校验；`cmd/dingtalk-robot/main.go` 的 `buildProvider` 加分支。
4. 文档：`docs/configuration.md` 与 `docs/channels.md`。
5. 测试：provider 单测 + e2e 装配。

注意：通道是启动时选定的，不支持按请求切换；一个实例只投递一个目标。

## 文档约定

- 三条单一事实源：配置键只在 [配置](configuration.md)，错误码与响应契约只在 [接口契约](api.md)，通道能力只在 [投递通道](channels.md)。其他文档只链接，不复述表格。
- 外部链接只在「拥有者文档」出现一次（源码与镜像在 README，钉钉文档在 channels/api，Watchtower 在接入文档）。README 是 Docker Hub 描述来源，必须用绝对 URL，`docs/` 内部用相对链接。
- 断言以代码为准：默认值来自 `config.Defaults()`，错误码来自 `apierr` 常量，指标名来自 `observability`，镜像参数来自 `Dockerfile` 与 CI。
- 验收计数：26 个配置键、18 个错误码、5 个指标、4 条路由、3 个 CLI 参数、5 种 `msgtype`、12 个日志字段、6 种错误分类。
- 命令必须能原样粘贴；会漂移的官方数字旁注明「以官方页面为准」。

## 本地常见问题

| 现象                                         | 处理                                                       |
| -------------------------------------------- | ---------------------------------------------------------- |
| `dingtalk.custom.access_token is required`   | 没给 token，或用了旧别名；服务只读 `DINGTALK_ACCESS_TOKEN` |
| `server.api_token is required`               | 补令牌或显式 `SERVER_ALLOW_UNAUTHENTICATED=true`           |
| `address already in use`                     | `SERVER_ADDR=:8081`                                        |
| `environment variable ... is not a duration` | 补单位，如 `10s`                                           |
| 显式 `-config` 报文件不存在                  | 路径写错；不传 `-config` 才会自动查找                      |
| 504 `upstream_timeout`                       | 本机到钉钉的网络/DNS 不通，或代理变量拦住了出站            |

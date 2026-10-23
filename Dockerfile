# syntax=docker/dockerfile:1

FROM golang:1.27.3-alpine3.24@sha256:d530cf60723abf1ffae0cd16bb546b379ec0550d2f3815e5b16ed69dbc00fba4 AS builder

WORKDIR /build
COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

COPY . /build/

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -mod=readonly -trimpath \
	-ldflags="-s -w -X main.version=${VERSION}" \
	-o app ./cmd/dingtalk-robot

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

LABEL maintainer="Jerry"

RUN apk add --no-cache tzdata && \
	cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
	echo "Asia/Shanghai" > /etc/timezone && \
	apk del tzdata

COPY --from=builder /build/app /usr/bin/dingtalk-robot
COPY --from=builder /build/config/example.config.yaml /config/config.yaml

# 以非 root 运行；配置文件只读挂载即可（进程不会改写它）。
RUN addgroup -g 65532 -S nonroot && \
	adduser -u 65532 -S -G nonroot -H -s /sbin/nologin nonroot

USER 65532:65532

ENV SERVER_ADDR=":8080"

EXPOSE 8080/tcp
VOLUME /config

# 健康检查由进程自身的 -healthcheck 子命令完成，镜像内不安装额外 HTTP 工具。
HEALTHCHECK --interval=15s --timeout=5s --retries=3 --start-period=5s \
	CMD ["/usr/bin/dingtalk-robot", "-healthcheck"]

ENTRYPOINT ["/usr/bin/dingtalk-robot"]

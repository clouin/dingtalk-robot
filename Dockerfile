FROM golang:1.19.5-alpine as builder

WORKDIR /build
COPY . /build/

RUN go mod tidy && go build -o app main.go

FROM alpine:3.17
LABEL maintainer = Jerry

RUN apk add --no-cache curl tzdata && \
	cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
	echo "Asia/Shanghai" > /etc/timezone && \
	apk del tzdata

COPY --from=builder /build/app /usr/bin/dingtalk-robot
COPY --from=builder /build/config/example.config.yaml /config/config.yaml

RUN chmod +x /usr/bin/dingtalk-robot

EXPOSE 8080/tcp
VOLUME /config

HEALTHCHECK --interval=15s --timeout=5s --retries=3 --start-period=5s CMD curl -f 127.0.0.1:8080

ENTRYPOINT ["/usr/bin/dingtalk-robot"]
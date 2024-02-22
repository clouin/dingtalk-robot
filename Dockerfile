FROM golang:1.19.5-alpine as builder

WORKDIR /build
COPY . /build/

RUN go mod tidy && go build -o app main.go

FROM alpine:3.17
LABEL maintainer = Jerry

RUN apk add tzdata && \
	cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
	echo "Asia/Shanghai" > /etc/timezone && \
	apk del tzdata

COPY --from=builder /build/app /usr/local/bin/dingtalk-robot
COPY --from=builder /build/config/example.config.yaml /config/config.yaml

RUN chmod +x /usr/local/bin/dingtalk-robot

EXPOSE 8080
VOLUME /config

ENTRYPOINT ["dingtalk-robot"]
FROM golang:1.19.5-alpine as builder
RUN mkdir /build
ADD . /build/
WORKDIR /build
RUN go mod tidy && go build -o dingtalk-robot main.go

FROM alpine:3.17
MAINTAINER Jerry

EXPOSE 8080

COPY --from=builder /build/dingtalk-robot /usr/local/bin/dingtalk-robot

RUN chmod +x /usr/local/bin/dingtalk-robot

ENTRYPOINT ["dingtalk-robot"]
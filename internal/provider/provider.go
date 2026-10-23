// Package provider 定义投递通道的抽象与错误分类。
package provider

import (
	"context"
	"net"
	"net/http"
	"time"

	"dingtalk-robot/internal/message"
)

// ErrorClass 是上游结果的分类，决定是否重试以及如何映射状态码。
type ErrorClass string

const (
	// ClassOK 表示投递成功。
	ClassOK ErrorClass = "ok"
	// ClassThrottled 表示被限流，不应重试。
	ClassThrottled ErrorClass = "throttled"
	// ClassRetryable 表示网络错误、超时或上游 5xx，可以退避重试。
	ClassRetryable ErrorClass = "retryable"
	// ClassAuth 表示上游鉴权失败，通道可失效令牌后重试一次。
	ClassAuth ErrorClass = "auth"
	// ClassConfig 表示签名、密钥、关键词等配置类错误，重试无意义。
	ClassConfig ErrorClass = "config"
	// ClassRejected 表示上游明确拒绝，重试无意义。
	ClassRejected ErrorClass = "rejected"
)

// CustomThrottleRetryAfter 是自定义机器人被判限流后的建议等待时长。
//
// 官方在超过每分钟 20 条后封禁 10 分钟，给出更短的建议值只会让调用方稳定撞上
// 仍在生效的封禁。企业通道的限流是 QPS 级抖动，等待时长由该通道自己给出。
const CustomThrottleRetryAfter = 10 * time.Minute

// Result 是一次投递的结果。
type Result struct {
	ErrCode int64
	// UpstreamCode 是上游原始字符串错误码。企业通道的业务码是字符串
	// （如 Forbidden.AccessDenied.QpsLimitForAppkeyAndApi），无法放进 ErrCode；
	// 当该字符串本身是数字时两个字段会同时给出同一个值。
	UpstreamCode string
	ErrMsg       string
	Class        ErrorClass
	// RetryAfter 是通道建议的等待时长，仅在限流类结果上有意义；为零表示
	// 该通道没有更具体的建议，由编排层回退到默认值。
	RetryAfter time.Duration
	AtDropped  bool
}

// Provider 是一条投递通道。
type Provider interface {
	// Name 返回通道名，用于日志与指标。
	Name() string
	// Validate 在投递前检查通道能力，返回的错误会直接映射为 400。
	Validate(m *message.Message) error
	// Send 执行一次投递（不含重试与限流）。
	Send(ctx context.Context, m *message.Message) Result
}

// NewHTTPClient 返回共享的 HTTP 客户端：连接复用、各阶段超时齐备。
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

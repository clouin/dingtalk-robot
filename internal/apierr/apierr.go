// Package apierr 定义可映射到 HTTP 响应的稳定错误类型。
//
// 错误码分为两类：稳定的本地 code（字符串，供调用方分支判断）与上游 errcode
// （数字，原样透传）。两者不共用命名空间，避免"参数错"与"钉钉拒绝"混淆。
package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Error 是一个携带 HTTP 状态码与稳定 code 的错误。
type Error struct {
	Status   int
	Code     string
	Message  string
	Upstream int64
	// UpstreamCode 是上游原始字符串错误码，与 Upstream 同源：上游码是数字时
	// 两者都填，是字符串时只有本字段有值。为空表示上游没有给出码。
	UpstreamCode string
	RetryAfter   time.Duration
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New 构造一个 apierr。
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// As 提取 *Error。
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// 稳定的本地错误码。
const (
	CodeInvalidJSON            = "invalid_json"
	CodeInvalidRequest         = "invalid_request"
	CodeInvalidMessage         = "invalid_message"
	CodeMessageTooLarge        = "message_too_large"
	CodeUnsupportedMessageType = "unsupported_message_type"
	// CodeUnsupportedMessageTypeForProvider 表示消息类型不被当前投递通道支持。
	CodeUnsupportedMessageTypeForProvider = "unsupported_message_type_for_provider"
	// CodeAtNotSupported 表示当前投递通道不支持 @ 且未开启忽略开关。
	CodeAtNotSupported  = "at_not_supported"
	CodeAtLimitExceeded = "at_limit_exceeded"
	CodeUnauthorized    = "unauthorized"
	CodeNotFound        = "not_found"
	// CodeMethodNotAllowed 表示路径存在但 HTTP 方法不被允许。
	CodeMethodNotAllowed = "method_not_allowed"
	// CodeOptionNotSupported 表示请求中的展示选项无法被当前通道表达。
	CodeOptionNotSupported   = "option_not_supported"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeRateLimited          = "rate_limited"
	CodeUpstreamRejected     = "upstream_rejected"
	CodeUpstreamTimeout      = "upstream_timeout"
	CodeInternal             = "internal"
)

// InvalidJSON 表示请求体无法解析为 JSON。
func InvalidJSON(message string) *Error {
	return New(http.StatusBadRequest, CodeInvalidJSON, message)
}

// InvalidRequest 表示请求语义不合法。
func InvalidRequest(message string) *Error {
	return New(http.StatusBadRequest, CodeInvalidRequest, message)
}

// InvalidMessage 表示消息内容不满足投递要求。
func InvalidMessage(message string) *Error {
	return New(http.StatusBadRequest, CodeInvalidMessage, message)
}

// UnsupportedMediaType 表示 Content-Type 不被接受。
func UnsupportedMediaType(message string) *Error {
	return New(http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, message)
}

// PayloadTooLarge 表示请求体超过上限。
func PayloadTooLarge(message string) *Error {
	return New(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, message)
}

// Unauthorized 表示入口鉴权失败。
func Unauthorized(message string) *Error {
	return New(http.StatusUnauthorized, CodeUnauthorized, message)
}

// NotFound 表示路由不存在。
func NotFound(message string) *Error {
	return New(http.StatusNotFound, CodeNotFound, message)
}

// RateLimited 表示本地或上游限流，并在 RetryAfter 中给出建议等待时长。
func RateLimited(message string, retryAfter time.Duration) *Error {
	err := New(http.StatusTooManyRequests, CodeRateLimited, message)
	err.RetryAfter = retryAfter
	return err
}

// UpstreamRejected 表示钉钉拒绝了本次投递，并保留其原始 errcode。
func UpstreamRejected(errcode int64, message string) *Error {
	err := New(http.StatusBadGateway, CodeUpstreamRejected, message)
	err.Upstream = errcode
	return err
}

// UpstreamTimeout 表示上游超时或重试预算耗尽。
func UpstreamTimeout(message string) *Error {
	return New(http.StatusGatewayTimeout, CodeUpstreamTimeout, message)
}

// Internal 表示服务内部错误。
func Internal(message string) *Error {
	return New(http.StatusInternalServerError, CodeInternal, message)
}

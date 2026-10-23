package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// routeUnmatched 是路由标签的兜底取值：既覆盖未知路径，也覆盖注册在 "/" 上的兜底处理。
const routeUnmatched = "unmatched"

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyRecord
	ctxKeyRecorder
)

// requestRecord 收集只在处理过程中才知道的日志字段。
type requestRecord struct {
	msgType  string
	provider string
	errCode  int64
	attempts int
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

func recordFrom(ctx context.Context) *requestRecord {
	record, _ := ctx.Value(ctxKeyRecord).(*requestRecord)
	if record == nil {
		return &requestRecord{}
	}
	return record
}

// recorderFrom 返回本次请求共享的响应记录器；未安装时返回 nil。
func recorderFrom(ctx context.Context) *statusRecorder {
	recorder, _ := ctx.Value(ctxKeyRecorder).(*statusRecorder)
	return recorder
}

// statusRecorder 记录响应状态码与字节数。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// withRequestID 解析或生成请求标识，并为本请求准备日志字段容器。
func (s *server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)

		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyRecord, &requestRecord{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRecover 兜住处理过程中的 panic。
//
// 它必须位于 accessLog 内侧：panic 会跳过 accessLog 中 next.ServeHTTP 之后的语句，
// 只有让 recover 在内层先写回 500 并正常返回，accessLog 才能照常记录这条失败请求。
// 判断"是否已开始写响应"用的是 accessLog 放进 context 的共享 recorder。
func (s *server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			s.deps.Logger.Error("panic while handling request",
				"request_id", requestIDFrom(r.Context()),
				"panic", recovered,
			)
			if recorder := recorderFrom(r.Context()); recorder != nil && recorder.status != 0 {
				return // 已经开始写响应，再写状态码只会污染已写出的内容
			}
			s.writeError(w, r, errInternalPanic())
		}()

		next.ServeHTTP(w, r)
	})
}

// withAccessLog 为业务请求输出一条访问日志，探针请求只记 debug。
//
// 它同时创建本请求的 statusRecorder 并放进 context，供 recover 判断响应是否已开始
// 写出、供 metrics 读取状态码。
func (s *server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		recorder := recorderFrom(r.Context())
		if recorder == nil {
			recorder = &statusRecorder{ResponseWriter: w}
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyRecorder, recorder))
			w = recorder
		}

		next.ServeHTTP(w, r)

		status := recorder.statusCode()
		written := recorder.bytes

		record := recordFrom(r.Context())
		attrs := []any{
			"request_id", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"route", s.routeOf(r),
			"status", status,
		}
		// 空字段不输出：探针与纯路由层拒绝没有消息类型、通道与上游错误码。
		if record.msgType != "" {
			attrs = append(attrs, "msgtype", record.msgType)
		}
		if record.provider != "" {
			attrs = append(attrs, "provider", record.provider)
		}
		if record.errCode != 0 {
			attrs = append(attrs, "errcode", record.errCode)
		}
		if record.attempts > 0 {
			attrs = append(attrs, "attempts", record.attempts)
		}
		attrs = append(attrs,
			"bytes", written,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_ip", clientIP(r),
		)

		switch {
		case isProbe(r.URL.Path):
			s.deps.Logger.Debug("request handled", attrs...)
		case status >= http.StatusInternalServerError:
			s.deps.Logger.Error("request failed", attrs...)
		default:
			s.deps.Logger.Info("request handled", attrs...)
		}
	})
}

func (s *server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProbe(r.URL.Path) || s.deps.APIToken == "" {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := bearerToken(r)
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.deps.APIToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dingtalk-robot"`)
			s.writeError(w, r, errUnauthorized())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) withMetrics(next http.Handler) http.Handler {
	if s.deps.Metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.deps.Metrics.IncInflight()
		defer s.deps.Metrics.DecInflight()

		next.ServeHTTP(w, r)

		status := http.StatusOK
		if recorder := recorderFrom(r.Context()); recorder != nil {
			status = recorder.statusCode()
		}
		s.deps.Metrics.IncRequest(s.routeOf(r), status)
	})
}

// routeOf 返回命中的路由模式，用于指标与日志的低基数标签。
//
// 鉴权等中间件会在 ServeMux 之前返回，此时 r.Pattern 尚未写入，因此回退到用
// 路由表再匹配一次。匹配的是同一个 mux 实例，不会出现两张路由表漂移。
func (s *server) routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return normalizeRoute(r.Pattern)
	}
	if s.mux == nil {
		return routeUnmatched
	}
	if _, pattern := s.mux.Handler(r); pattern != "" {
		return normalizeRoute(pattern)
	}
	return routeUnmatched
}

// normalizeRoute 把兜底路由 "/" 归一化为 unmatched，使 404 与"未命中"同义。
func normalizeRoute(pattern string) string {
	if pattern == "" || pattern == "/" {
		return routeUnmatched
	}
	return pattern
}

func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if idx := strings.IndexByte(forwarded, ','); idx > 0 {
			return strings.TrimSpace(forwarded[:idx])
		}
		return strings.TrimSpace(forwarded)
	}
	host := r.RemoteAddr
	if idx := strings.LastIndexByte(host, ':'); idx > 0 {
		return host[:idx]
	}
	return host
}

func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000")
	}
	return hex.EncodeToString(buf[:])
}

// sanitizeRequestID 只接受短小的可打印 id，避免把任意头内容写进日志。
func sanitizeRequestID(id string) string {
	if id == "" || len(id) > 64 {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ""
		}
	}
	return id
}

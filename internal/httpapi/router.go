// Package httpapi 实现 HTTP 入口：路由、中间件与响应契约。
package httpapi

import (
	"log/slog"
	"net/http"

	"dingtalk-robot/internal/observability"
	"dingtalk-robot/internal/relay"
)

// Deps 是构造入口所需的依赖。
type Deps struct {
	Relay        *relay.Relay
	APIToken     string
	MaxBodyBytes int64
	Ready        func() bool
	Metrics      *observability.Metrics
	Logger       *slog.Logger
}

// NewRouter 组装路由与中间件链。
//
// 链顺序（外到内）：requestID → accessLog → metrics → recover → auth → mux。
// recover 必须在 accessLog 内侧：panic 会跳过 accessLog 的收尾语句，只有先在内层
// 写回 500 并正常返回，访问日志才能记录到这条失败请求。
// metrics 必须在 auth 外侧：鉴权拒绝的请求同样要被计数，否则令牌配错时指标看不出来。
func NewRouter(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = 65536
	}

	// 故意不使用 "METHOD /path" 模式：那会让方法不匹配由 ServeMux 以纯文本应答，
	// 破坏统一的 JSON 契约。这里统一由 allowMethod 与 notFound 产出 JSON。
	mux := http.NewServeMux()
	s := &server{deps: d, mux: mux}

	mux.Handle("/v1/messages", s.allowMethod(http.MethodPost, s.sendHandler()))
	mux.Handle("/healthz", s.allowMethod(http.MethodGet, http.HandlerFunc(s.health)))
	mux.Handle("/readyz", s.allowMethod(http.MethodGet, http.HandlerFunc(s.readiness)))
	if d.Metrics != nil {
		mux.Handle("/metrics", s.allowMethod(http.MethodGet, d.Metrics.Handler()))
	}
	mux.Handle("/", http.HandlerFunc(s.notFound))

	var handler http.Handler = mux
	handler = s.withAuth(handler)
	handler = s.withRecover(handler)
	handler = s.withMetrics(handler)
	handler = s.withAccessLog(handler)
	handler = s.withRequestID(handler)
	return handler
}

type server struct {
	deps Deps
	mux  *http.ServeMux
}

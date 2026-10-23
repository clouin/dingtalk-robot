package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 汇总服务暴露的 Prometheus 指标。
type Metrics struct {
	registry   *prometheus.Registry
	requests   *prometheus.CounterVec
	deliveries *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	inflight   prometheus.Gauge
	rateLimit  *prometheus.CounterVec
}

// NewMetrics 创建指标集与其独立注册表。
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_robot_requests_total",
			Help: "Total number of HTTP requests handled by route and status code.",
		}, []string{"route", "status"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_robot_deliveries_total",
			Help: "Total number of delivery attempts by provider and result class.",
		}, []string{"provider", "class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dingtalk_robot_delivery_duration_seconds",
			Help:    "Delivery attempt latency in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"provider"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dingtalk_robot_inflight_requests",
			Help: "Number of requests currently being handled.",
		}),
		rateLimit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_robot_ratelimit_events_total",
			Help: "Rate limit events by action.",
		}, []string{"action"}),
	}

	m.registry.MustRegister(
		m.requests, m.deliveries, m.duration, m.inflight, m.rateLimit,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler 返回指标抓取端点。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// IncRequest 记录一次请求；状态码标签用三位数字码，便于聚合与告警表达式。
func (m *Metrics) IncRequest(route string, status int) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Inc()
}

// ObserveDelivery 记录一次投递尝试。
func (m *Metrics) ObserveDelivery(provider, class string, duration time.Duration) {
	m.deliveries.WithLabelValues(provider, class).Inc()
	m.duration.WithLabelValues(provider).Observe(duration.Seconds())
}

// IncRateLimit 记录一次限流事件。
func (m *Metrics) IncRateLimit(action string) {
	m.rateLimit.WithLabelValues(action).Inc()
}

// IncInflight 增加在途请求计数。
func (m *Metrics) IncInflight() { m.inflight.Inc() }

// DecInflight 减少在途请求计数。
func (m *Metrics) DecInflight() { m.inflight.Dec() }

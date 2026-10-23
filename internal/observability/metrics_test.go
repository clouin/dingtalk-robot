package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsUseServiceNamespace(t *testing.T) {
	metrics := NewMetrics()
	metrics.IncRequest("/v1/messages", http.StatusOK)
	metrics.ObserveDelivery("custom", "ok", 120*time.Millisecond)
	metrics.IncRateLimit("rejected")
	metrics.IncInflight()

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		`dingtalk_robot_requests_total{route="/v1/messages",status="200"}`,
		`dingtalk_robot_deliveries_total{class="ok",provider="custom"}`,
		`dingtalk_robot_delivery_duration_seconds_count{provider="custom"}`,
		`dingtalk_robot_ratelimit_events_total{action="rejected"}`,
		`dingtalk_robot_inflight_requests`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}

	if strings.Contains(body, "relay_") {
		t.Error("metrics output still carries the old relay_ prefix")
	}
	if strings.Contains(body, `status="OK"`) {
		t.Error("status label must use the numeric code, not the reason phrase")
	}
}

package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"dingtalk-robot/internal/apierr"
)

// response 是统一响应体。
type response struct {
	OK        bool   `json:"ok"`
	Code      string `json:"code,omitempty"`
	ErrMsg    string `json:"errmsg"`
	ErrCode   int64  `json:"errcode"`
	RequestID string `json:"request_id"`
	Provider  string `json:"provider"`
	AtDropped *bool  `json:"at_dropped,omitempty"`
	// UpstreamCode 是上游原始字符串错误码，仅在上游给出时出现。
	UpstreamCode string `json:"upstream_code,omitempty"`
}

func (s *server) writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.deps.Logger.Debug("write response failed", "error", err)
	}
}

// writeError 把任意错误映射为对外响应。
func (s *server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr, ok := apierr.As(err)
	if !ok {
		apiErr = apierr.Internal(err.Error())
	}

	status := apiErr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}

	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dingtalk-robot"`)
	}
	if apiErr.RetryAfter > 0 {
		seconds := int(apiErr.RetryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}

	s.writeJSON(w, status, response{
		OK:           false,
		Code:         apiErr.Code,
		ErrMsg:       apiErr.Message,
		ErrCode:      apiErr.Upstream,
		UpstreamCode: apiErr.UpstreamCode,
		RequestID:    requestIDFrom(r.Context()),
		Provider:     s.providerName(),
	})
}

func errUnauthorized() error {
	return apierr.Unauthorized("missing or invalid bearer token")
}

func errInternalPanic() error {
	return apierr.Internal("internal error")
}

func (s *server) providerName() string {
	if s.deps.Relay == nil {
		return ""
	}
	return s.deps.Relay.ProviderName()
}

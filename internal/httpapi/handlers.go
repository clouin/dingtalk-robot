package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/ingest"
)

// notFound 处理未注册的路径。
func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, apierr.NotFound("no route matches "+r.URL.Path))
}

// allowMethod 只放行指定方法，其余方法返回统一的 405 JSON。
func (s *server) allowMethod(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			s.writeError(w, r, apierr.New(http.StatusMethodNotAllowed,
				apierr.CodeMethodNotAllowed,
				fmt.Sprintf("method %s is not allowed on %s", r.Method, r.URL.Path)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// health 是存活探针：不依赖任何外部服务。
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// readiness 是就绪探针：进入停机流程后返回 503。
func (s *server) readiness(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Ready != nil && !s.deps.Ready() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("draining\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// sendHandler 处理消息投递。
func (s *server) sendHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := recordFrom(r.Context())

		if err := requireJSON(r); err != nil {
			s.writeError(w, r, err)
			return
		}

		limit := s.deps.MaxBodyBytes
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				s.writeError(w, r, apierr.PayloadTooLarge("request body exceeds the configured limit"))
				return
			}
			s.writeError(w, r, apierr.InvalidRequest("cannot read request body: "+err.Error()))
			return
		}

		msg, err := ingest.Parse(body)
		if msg != nil {
			record.msgType = string(msg.Type)
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}

		record.provider = s.providerName()

		outcome, err := s.deps.Relay.Send(r.Context(), msg)
		record.errCode = outcome.Result.ErrCode
		record.attempts = outcome.Attempts
		if err != nil {
			s.writeError(w, r, err)
			return
		}

		body200 := response{
			OK:        true,
			ErrMsg:    "ok",
			ErrCode:   0,
			RequestID: requestIDFrom(r.Context()),
			Provider:  record.provider,
		}
		if outcome.Result.AtDropped {
			dropped := true
			body200.AtDropped = &dropped
		}
		s.writeJSON(w, http.StatusOK, body200)
	})
}

// requireJSON 校验 Content-Type。
func requireJSON(r *http.Request) error {
	raw := r.Header.Get("Content-Type")
	if raw == "" {
		return apierr.UnsupportedMediaType("Content-Type must be application/json")
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || mediaType != "application/json" {
		return apierr.UnsupportedMediaType("Content-Type must be application/json, got " + raw)
	}
	return nil
}

package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/platform/logx"
)

type principalKey struct{}

func principalFrom(ctx context.Context) authz.Principal {
	p, _ := ctx.Value(principalKey{}).(authz.Principal)
	return p
}

// statusRecorder ghi lại mã trả về để log và metric.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// observe: gắn trace, đo thời gian, ghi một dòng log cho mỗi request (không có body,
// không có token, không có nội dung hóa đơn).
func (s *Server) observe(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		t := parseTrace(r)
		ctx := logx.WithTrace(r.Context(), t)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				s.log.ErrorContext(ctx, "panic", "route", route, "panic", v)
				s.writeError(rec, r.WithContext(ctx), apperr.New(apperr.CodeInternal, "Có lỗi xảy ra."))
			}
			s.log.InfoContext(ctx, "http",
				"route", route, "status", rec.status,
				"latency_ms", time.Since(start).Milliseconds())
			s.metrics.ObserveHTTP(route, rec.status, time.Since(start))
		}()
		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// parseTrace đọc W3C traceparent, dự phòng X-Cloud-Trace-Context của Google front end.
func parseTrace(r *http.Request) logx.Trace {
	if tp := r.Header.Get("traceparent"); tp != "" {
		parts := strings.Split(tp, "-")
		if len(parts) == 4 && len(parts[1]) == 32 {
			return logx.Trace{ID: parts[1], SpanID: parts[2]}
		}
	}
	if xc := r.Header.Get("X-Cloud-Trace-Context"); xc != "" {
		id, rest, _ := strings.Cut(xc, "/")
		span, _, _ := strings.Cut(rest, ";")
		return logx.Trace{ID: id, SpanID: span}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return logx.Trace{ID: hex.EncodeToString(b)}
}

// authenticate: App Check rồi Firebase ID token, rồi nạp Principal từ users/{uid}.
// Thiếu hoặc sai token đều trả 401 để app quay về màn đăng nhập (spec mục 1).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if s.appCheck != nil {
			tok := r.Header.Get("X-Firebase-AppCheck")
			if tok == "" || s.appCheck.VerifyAppCheck(ctx, tok) != nil {
				s.writeError(w, r, apperr.New(apperr.CodeAppCheck, "Ứng dụng chưa được xác thực."))
				return
			}
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			s.writeError(w, r, apperr.ErrUnauthenticated)
			return
		}
		uid, err := s.tokens.VerifyIDToken(ctx, raw)
		if err != nil {
			s.writeError(w, r, apperr.Wrap(apperr.CodeUnauthenticated, apperr.ErrUnauthenticated.Message, err))
			return
		}
		p, err := s.principals.LoadPrincipal(ctx, uid)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
	})
}

// limitWrites: giới hạn số thao tác ghi mỗi phút cho mỗi người dùng, trên mỗi instance
// (tối đa 3 instance). Không thay quota hóa đơn, chỉ chặn spam và client lỗi lặp vô hạn.
func (s *Server) limitWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !s.limiter.allow(principalFrom(r.Context()).UID, time.Now()) {
			s.writeError(w, r, apperr.New(apperr.CodeRateLimited, "Thao tác quá nhanh, đợi một chút rồi thử lại."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// windowLimiter: cửa sổ cố định một phút, bộ nhớ có giới hạn.
type windowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Time
	counts  map[string]int
	maxKeys int
}

func newWindowLimiter(limit int) *windowLimiter {
	return &windowLimiter{limit: limit, counts: map[string]int{}, maxKeys: 10_000}
}

func (l *windowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := now.Truncate(time.Minute)
	if !w.Equal(l.window) || len(l.counts) > l.maxKeys {
		l.window = w
		clear(l.counts)
	}
	l.counts[key]++
	return l.counts[key] <= l.limit
}

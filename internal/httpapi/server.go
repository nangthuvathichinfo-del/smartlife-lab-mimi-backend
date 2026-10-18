// Package httpapi là lớp HTTP của mimi-api: định tuyến, xác thực, giải mã request,
// gọi service và trả lỗi theo mã ổn định trong api/openapi.yaml. Không chứa logic
// nghiệp vụ.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

// Các phụ thuộc được khai báo ở phía dùng, nhỏ nhất có thể.
type (
	TokenVerifier interface {
		VerifyIDToken(ctx context.Context, idToken string) (uid string, err error)
	}
	AppCheckVerifier interface {
		VerifyAppCheck(ctx context.Context, token string) error
	}
	PrincipalLoader interface {
		LoadPrincipal(ctx context.Context, uid string) (authz.Principal, error)
	}
	Profiles interface {
		Profile(ctx context.Context, p authz.Principal) (user.Profile, error)
	}
	Receipts interface {
		Read(ctx context.Context, p authz.Principal, receiptID, lang string, imgs []receipt.Image) (receipt.Draft, error)
		Save(ctx context.Context, p authz.Principal, req receipt.SaveRequest) (receipt.SaveOutcome, error)
	}
	Items interface {
		List(ctx context.Context, p authz.Principal, q item.Query) ([]item.Item, error)
		ListShared(ctx context.Context, p authz.Principal, horizon int) ([]item.SharedItem, error)
		ChangeStock(ctx context.Context, p authz.Principal, key string, c item.StockChange) (item.Item, error)
		UndoStock(ctx context.Context, p authz.Principal, key string, actedAt time.Time) (item.Item, error)
	}
	ReadinessChecker interface {
		Ready(ctx context.Context) error
	}
	Metrics interface {
		ObserveHTTP(route string, status int, d time.Duration)
	}
)

// Deps gom các phụ thuộc bắt buộc; nối thủ công trong cmd/mimi.
type Deps struct {
	Log        *slog.Logger
	Tokens     TokenVerifier
	AppCheck   AppCheckVerifier // nil khi chạy với emulator ở máy
	Principals PrincipalLoader
	Profiles   Profiles
	Receipts   Receipts
	Items      Items
	Ready      ReadinessChecker
	Metrics    Metrics // nil: không ghi metric
}

type Server struct {
	log        *slog.Logger
	tokens     TokenVerifier
	appCheck   AppCheckVerifier
	principals PrincipalLoader
	profiles   Profiles
	receipts   Receipts
	items      Items
	ready      ReadinessChecker
	metrics    Metrics
	limiter    *windowLimiter

	readTimeout    time.Duration
	defaultTimeout time.Duration
}

type Option func(*Server)

// WithTimeouts đặt ngân sách thời gian: readTimeout cho POST /receipts/read (dưới
// 60 s của Hosting rewrite), defaultTimeout cho mọi route khác.
func WithTimeouts(read, def time.Duration) Option {
	return func(s *Server) { s.readTimeout, s.defaultTimeout = read, def }
}

func WithWriteLimit(perMinute int) Option {
	return func(s *Server) { s.limiter = newWindowLimiter(perMinute) }
}

func New(d Deps, opts ...Option) (*Server, error) {
	if d.Log == nil || d.Tokens == nil || d.Principals == nil || d.Receipts == nil || d.Items == nil || d.Ready == nil || d.Profiles == nil {
		return nil, errors.New("httpapi: missing required dependency")
	}
	s := &Server{
		log: d.Log, tokens: d.Tokens, appCheck: d.AppCheck, principals: d.Principals,
		profiles: d.Profiles, receipts: d.Receipts, items: d.Items, ready: d.Ready, metrics: d.Metrics,
		limiter: newWindowLimiter(60), readTimeout: 55 * time.Second, defaultTimeout: 10 * time.Second,
	}
	if s.metrics == nil {
		s.metrics = noopMetrics{}
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

type noopMetrics struct{}

func (noopMetrics) ObserveHTTP(string, int, time.Duration) {}

// Handler trả http.Handler hoàn chỉnh. Firebase Hosting rewrite giữ nguyên tiền tố
// /api, nên router nhận cả hai dạng có và không có /api.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	public := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.observe(pattern, h))
	}
	private := func(pattern string, timeout time.Duration, h http.HandlerFunc) {
		var chain http.Handler = h
		chain = s.limitWrites(chain)
		chain = s.authenticate(chain)
		chain = http.TimeoutHandler(chain, timeout, `{"code":"timeout","message":"Hết thời gian xử lý, thử lại."}`)
		mux.Handle(pattern, s.observe(pattern, chain))
	}

	public("GET /healthz", s.healthz)
	public("GET /readyz", s.readyz)

	private("GET /me", s.defaultTimeout, s.getMe)
	private("POST /receipts/read", s.readTimeout, s.readReceipt)
	private("POST /receipts", s.defaultTimeout, s.saveReceipt)
	private("GET /items", s.defaultTimeout, s.listItems)
	private("PATCH /items/{key}/stock", s.defaultTimeout, s.changeStock)
	private("DELETE /items/{key}/override", s.defaultTimeout, s.undoStock)
	private("GET /households/needs", s.defaultTimeout, s.listSharedNeeds)

	// Các endpoint còn lại đã có trong api/openapi.yaml nhưng chưa dựng ở khung này.
	for _, p := range notYetImplemented {
		private(p, s.defaultTimeout, s.notImplemented)
	}

	root := http.NewServeMux()
	root.Handle("/api/", http.StripPrefix("/api", mux))
	root.Handle("/", mux)
	return root
}

var notYetImplemented = []string{
	"GET /home", "PUT /settings", "DELETE /me",
	"GET /needs", "GET /items/{key}", "PATCH /items/{key}",
	"PATCH /receipts/{id}/lines/{lineId}", "GET /receipts",
	"GET /spending", "GET /forecast",
	"GET /recurring", "POST /recurring", "PATCH /recurring/{id}", "DELETE /recurring/{id}", "POST /recurring/scan",
	"GET /plan", "PUT /plan/items/{key}", "DELETE /plan/items/{key}", "PUT /plan/state", "GET /plan/prices",
	"GET /households", "PATCH /households", "POST /households/invites", "POST /households/join",
	"DELETE /households/members/{uid}", "POST /households/leave",
	"POST /households/asks", "POST /households/asks/{id}/bought",
	"GET /notifications",
}

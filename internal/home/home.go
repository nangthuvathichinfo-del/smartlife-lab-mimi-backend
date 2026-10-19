// Package home dựng số phụ cho 4 ô Trang chủ trong một lần gọi (GET /home).
// Mọi con số do server tính; client chỉ định dạng.
package home

import (
	"context"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/item"
)

// Raw là dữ liệu thô đọc từ Firestore cho một người trong một tháng.
type Raw struct {
	SpentVND     int64 // months/{YYYY-MM}.spent_vnd
	MonthHasData bool  // tài liệu tháng tồn tại và có ít nhất một hóa đơn
	ReceiptCount int   // users/{uid}.receipt_count
}

// Store đọc dữ liệu thô. uid luôn lấy từ Principal.
type Store interface {
	HomeRaw(ctx context.Context, uid, month string) (Raw, error)
}

// PlanSummary trả số món trong giỏ, ước tính và số món chưa có giá (cùng cách tính với Giỏ).
type PlanSummary interface {
	Summary(ctx context.Context, p authz.Principal) (count int, estimate *int64, missingPrice int, err error)
}

// NeedsCounter đếm món "Cần mua" theo đúng quy tắc của màn Đồ dùng.
type NeedsCounter interface {
	CountNeeds(ctx context.Context, p authz.Principal, horizon int) (int, error)
}

type Plan struct {
	ItemCount    int    `json:"item_count"`
	Estimate     *int64 `json:"estimate"`      // null: chưa có giá để ước tính
	MissingPrice int    `json:"missing_price"` // số món trong giỏ chưa có giá
}

// Summary là phản hồi GET /home.
type Summary struct {
	SpentThisMonth      *int64 `json:"spent_this_month"` // null: tháng chưa có hóa đơn, hoặc người gọi là thành viên nhà
	NeedsCount          int    `json:"needs_count"`
	Plan                Plan   `json:"plan"`
	ReceiptCount        int    `json:"receipt_count"`
	UnreadNotifications int    `json:"unread_notifications"`
}

type Service struct {
	store Store
	needs NeedsCounter
	plan  PlanSummary
	now   func() time.Time
}

func New(store Store, needs NeedsCounter, plan PlanSummary, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, needs: needs, plan: plan, now: now}
}

// Get trả số phụ Trang chủ. Thành viên nhà không thấy chi tiêu hay giỏ của chủ nhà
// (spec mục 2): hai trường đó luôn rỗng với vai trò member.
func (s *Service) Get(ctx context.Context, p authz.Principal) (Summary, error) {
	if err := authz.Can(p, authz.ActionReadSelf); err != nil {
		return Summary{}, err
	}
	needs, err := s.needs.CountNeeds(ctx, p, item.NeedsHorizonDays)
	if err != nil {
		return Summary{}, err
	}
	month := domain.MonthOf(s.now(), domain.Location(p.Timezone))
	raw, err := s.store.HomeRaw(ctx, p.UID, month)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{NeedsCount: needs, ReceiptCount: raw.ReceiptCount}
	if authz.Can(p, authz.ActionReadSpending) == nil && raw.MonthHasData {
		spent := raw.SpentVND
		out.SpentThisMonth = &spent
	}
	if authz.Can(p, authz.ActionReadPlan) == nil {
		n, est, missing, err := s.plan.Summary(ctx, p)
		if err != nil {
			return Summary{}, err
		}
		out.Plan = Plan{ItemCount: n, Estimate: est, MissingPrice: missing}
	}
	return out, nil
}

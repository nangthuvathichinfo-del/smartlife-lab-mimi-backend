// Package item phục vụ Đồ dùng, Cần mua, Chi tiết món (spec mục 4, 5).
// Trạng thái và số ngày còn lại luôn được tính lúc đọc từ ngày dự kiến hết đã lưu,
// nên không bao giờ "cũ" theo thời gian.
package item

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

// Record là tài liệu users/{uid}/items/{group_key} đã lưu.
type Record struct {
	GroupKey      string
	Name          string
	NameNorm      string
	CategoryKey   string
	Prediction    domain.Prediction
	Override      *domain.Override
	LastPurchase  domain.Date
	Dropped       bool
	AskLaterUntil *domain.Date
}

// Store đọc ghi items. Mọi hàm nhận uid của Principal, không nhận uid từ request.
type Store interface {
	ListItems(ctx context.Context, uid string) ([]Record, error)
	GetItem(ctx context.Context, uid, groupKey string) (Record, error) // apperr.ErrNotFound nếu không có
	// SetOverride ghi bản sửa tay trong transaction: chỉ ghi nếu ov.SetAt mới hơn bản đang
	// có (lần bấm sau thắng), rồi tính lại Prediction bằng domain.Predict.
	SetOverride(ctx context.Context, uid, groupKey string, ov domain.Override) (Record, error)
	ClearOverride(ctx context.Context, uid, groupKey string, setAt time.Time) (Record, error)
	// OwnerOf trả uid chủ nhà để thành viên đọc danh sách dùng chung.
	OwnerOf(ctx context.Context, householdID string) (string, error)
}

// Item là dữ liệu đầy đủ cho chủ giỏ.
type Item struct {
	Key           string        `json:"key"`
	Name          string        `json:"name"`
	CategoryKey   string        `json:"category_key"`
	Status        domain.Status `json:"status"`
	DaysLeft      *int          `json:"days_left"`
	OutOn         string        `json:"predicted_out_on,omitempty"`
	IntervalDays  int           `json:"interval_days,omitempty"`
	PurchaseCount int           `json:"purchase_count"`
	Confidence    string        `json:"confidence,omitempty"`
	Source        string        `json:"source"`
}

// SharedItem là kiểu riêng cho thành viên nhà: KHÔNG có trường giá hay chi tiêu nào,
// nên quên lọc cũng không thể làm lộ giá (spec mục 2, 10).
type SharedItem struct {
	Key      string        `json:"key"`
	Name     string        `json:"name"`
	Status   domain.Status `json:"status"`
	DaysLeft *int          `json:"days_left"`
}

type Query struct {
	Tab     string // "soon" | "active"
	Horizon int    // 3 | 7 | 14
	Search  string
}

func (q Query) Validate() error {
	if q.Tab != "soon" && q.Tab != "active" {
		return apperr.New(apperr.CodeValidation, "Tab không hợp lệ.").With("field", "tab")
	}
	if q.Horizon != 3 && q.Horizon != 7 && q.Horizon != 14 {
		return apperr.New(apperr.CodeValidation, "Khoảng ngày chỉ là 3, 7 hoặc 14.").With("field", "horizon")
	}
	if len(q.Search) > 80 {
		return apperr.New(apperr.CodeValidation, "Từ khóa quá dài.").With("field", "q")
	}
	return nil
}

type Service struct {
	store Store
	now   func() time.Time
}

func New(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// List trả danh sách cho chủ giỏ.
func (s *Service) List(ctx context.Context, p authz.Principal, q Query) ([]Item, error) {
	if err := authz.Can(p, authz.ActionReadItemsFull); err != nil {
		return nil, err
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	recs, err := s.store.ListItems(ctx, p.UID)
	if err != nil {
		return nil, err
	}
	today := domain.Today(s.now(), loc(p))
	needle := domain.NormalizeVi(q.Search)
	out := make([]Item, 0, len(recs))
	for _, r := range recs {
		if r.Dropped || (needle != "" && !strings.Contains(r.NameNorm, needle)) {
			continue
		}
		v := domain.ViewAt(r.Prediction, today, lead(p))
		if q.Tab == "soon" && !domain.InNeeds(v, q.Horizon) {
			continue
		}
		out = append(out, toItem(r, v))
	}
	sortByDays(out)
	return out, nil
}

// ListShared trả danh sách món sắp hết của nhà cho thành viên, dạng rút gọn.
func (s *Service) ListShared(ctx context.Context, p authz.Principal, horizon int) ([]SharedItem, error) {
	if err := authz.Can(p, authz.ActionReadItemsShared); err != nil {
		return nil, err
	}
	if p.HouseholdID == "" {
		return []SharedItem{}, nil
	}
	ownerUID, err := s.store.OwnerOf(ctx, p.HouseholdID)
	if err != nil {
		return nil, err
	}
	recs, err := s.store.ListItems(ctx, ownerUID)
	if err != nil {
		return nil, err
	}
	today := domain.Today(s.now(), loc(p))
	out := []SharedItem{}
	for _, r := range recs {
		v := domain.ViewAt(r.Prediction, today, lead(p))
		if r.Dropped || !domain.InNeeds(v, horizon) {
			continue
		}
		out = append(out, SharedItem{Key: r.GroupKey, Name: r.Name, Status: v.Status, DaysLeft: v.DaysLeft})
	}
	return out, nil
}

// StockChange là thao tác "Còn nhiều / Sắp hết / Đã hết" hoặc nhập số ngày.
// ActedAt là lúc người dùng bấm (client gửi), để thao tác xếp hàng offline gửi muộn
// không đè lên thao tác mới hơn.
type StockChange struct {
	Quick   string    `json:"quick,omitempty"` // plenty | soon | out
	Days    *int      `json:"days,omitempty"`  // 1..60
	ActedAt time.Time `json:"acted_at"`
}

// maxClockSkew: chấp nhận đồng hồ máy người dùng lệch tối đa chừng này.
const maxClockSkew = 5 * time.Minute

func (s *Service) ChangeStock(ctx context.Context, p authz.Principal, key string, c StockChange) (Item, error) {
	if err := authz.Can(p, authz.ActionWriteItems); err != nil {
		return Item{}, err
	}
	now := s.now()
	if c.ActedAt.IsZero() || c.ActedAt.After(now.Add(maxClockSkew)) {
		return Item{}, apperr.New(apperr.CodeValidation, "Thời điểm thao tác không hợp lệ.").With("field", "acted_at")
	}
	setOn := domain.Today(c.ActedAt, loc(p))
	var ov domain.Override
	var ok bool
	switch {
	case c.Quick != "" && c.Days == nil:
		ov, ok = domain.OverrideFromQuick(c.Quick, setOn, c.ActedAt)
	case c.Quick == "" && c.Days != nil:
		ov, ok = domain.OverrideFromDays(*c.Days, setOn, c.ActedAt)
	}
	if !ok {
		return Item{}, apperr.New(apperr.CodeValidation, "Chọn Còn nhiều, Sắp hết, Đã hết hoặc nhập 1 đến 60 ngày.")
	}
	rec, err := s.store.SetOverride(ctx, p.UID, key, ov)
	if err != nil {
		return Item{}, err
	}
	return toItem(rec, domain.ViewAt(rec.Prediction, domain.Today(now, loc(p)), lead(p))), nil
}

// UndoStock xóa bản sửa tay (Hoàn tác). Không nhận cả tài liệu từ client.
func (s *Service) UndoStock(ctx context.Context, p authz.Principal, key string, actedAt time.Time) (Item, error) {
	if err := authz.Can(p, authz.ActionWriteItems); err != nil {
		return Item{}, err
	}
	rec, err := s.store.ClearOverride(ctx, p.UID, key, actedAt)
	if err != nil {
		return Item{}, err
	}
	return toItem(rec, domain.ViewAt(rec.Prediction, domain.Today(s.now(), loc(p)), lead(p))), nil
}

func toItem(r Record, v domain.View) Item {
	it := Item{
		Key: r.GroupKey, Name: r.Name, CategoryKey: r.CategoryKey,
		Status: v.Status, DaysLeft: v.DaysLeft, PurchaseCount: r.Prediction.PurchaseCount,
		IntervalDays: r.Prediction.IntervalDays, Confidence: string(r.Prediction.Confidence),
		Source: string(r.Prediction.Source),
	}
	if !r.Prediction.OutOn.IsZero() {
		it.OutOn = r.Prediction.OutOn.String()
	}
	return it
}

func sortByDays(xs []Item) {
	val := func(i Item) int {
		if i.DaysLeft == nil {
			return 1 << 30
		}
		return *i.DaysLeft
	}
	sort.SliceStable(xs, func(a, b int) bool {
		if val(xs[a]) != val(xs[b]) {
			return val(xs[a]) < val(xs[b])
		}
		return xs[a].Name < xs[b].Name
	})
}

func lead(p authz.Principal) int {
	if p.LeadDays >= 1 && p.LeadDays <= 30 {
		return p.LeadDays
	}
	return 7
}

func loc(p authz.Principal) *time.Location {
	if p.Timezone != "" {
		if l, err := time.LoadLocation(p.Timezone); err == nil {
			return l
		}
	}
	return time.FixedZone("ICT", 7*3600)
}

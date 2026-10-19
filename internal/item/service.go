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
	// History trả các lần mua thói quen gần nhất của một nhóm, mới nhất trước
	// (truy vấn purchases theo index group_key + habit + purchased_on).
	History(ctx context.Context, uid, groupKey string, limit int) ([]HistoryEntry, error)
}

// HistoryEntry là một lần mua trong Chi tiết món. Chỉ chủ giỏ đọc được (có giá).
type HistoryEntry struct {
	On        string `json:"on"` // YYYY-MM-DD
	Store     string `json:"store,omitempty"`
	AmountVND *int64 `json:"amount_vnd"` // nil khi dòng chưa quy đổi được sang VND
	ReceiptID string `json:"receipt_id,omitempty"`
}

// Detail là phản hồi GET /items/{key}: mọi trường của Item kèm lịch sử mua.
type Detail struct {
	Item
	History []HistoryEntry `json:"history"`
}

// historyLimit: số lần mua hiển thị trong Chi tiết món.
const historyLimit = 10

// NeedsHorizonDays là khoảng ngày mặc định của "Cần mua" và số trên ô Trang chủ.
const NeedsHorizonDays = 7

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

// Get trả Chi tiết món cho chủ giỏ, kèm tối đa historyLimit lần mua gần nhất.
func (s *Service) Get(ctx context.Context, p authz.Principal, key string) (Detail, error) {
	if err := authz.Can(p, authz.ActionReadItemsFull); err != nil {
		return Detail{}, err
	}
	if !ValidKey(key) {
		return Detail{}, apperr.ErrNotFound
	}
	rec, err := s.store.GetItem(ctx, p.UID, key)
	if err != nil {
		return Detail{}, err
	}
	hist, err := s.store.History(ctx, p.UID, key, historyLimit)
	if err != nil {
		return Detail{}, err
	}
	if hist == nil {
		hist = []HistoryEntry{} // JSON trả [] thay vì null
	}
	v := domain.ViewAt(rec.Prediction, domain.Today(s.now(), loc(p)), lead(p))
	return Detail{Item: toItem(rec, v), History: hist}, nil
}

// CountNeeds đếm món trong "Cần mua" với khoảng ngày horizon, cùng quy tắc với màn
// Đồ dùng (tab "soon"). Thành viên nhà đếm trên danh sách dùng chung của chủ nhà.
func (s *Service) CountNeeds(ctx context.Context, p authz.Principal, horizon int) (int, error) {
	if p.Role == authz.RoleMember {
		shared, err := s.ListShared(ctx, p, horizon)
		if err != nil {
			return 0, err
		}
		return len(shared), nil
	}
	if err := authz.Can(p, authz.ActionReadItemsFull); err != nil {
		return 0, err
	}
	recs, err := s.store.ListItems(ctx, p.UID)
	if err != nil {
		return 0, err
	}
	today := domain.Today(s.now(), loc(p))
	n := 0
	for _, r := range recs {
		if !r.Dropped && domain.InNeeds(domain.ViewAt(r.Prediction, today, lead(p)), horizon) {
			n++
		}
	}
	return n, nil
}

// ValidKey kiểm tra khóa nhóm trước khi dùng làm ID tài liệu Firestore: không rỗng,
// không quá dài, không chứa "/", không phải "." hay "..", không có dạng __x__ (ID dành riêng).
func ValidKey(key string) bool {
	if key == "" || len(key) > 128 || key == "." || key == ".." || strings.Contains(key, "/") {
		return false
	}
	return !(strings.HasPrefix(key, "__") && strings.HasSuffix(key, "__"))
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
	if !ValidKey(key) {
		return Item{}, apperr.ErrNotFound
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
	if !ValidKey(key) {
		return Item{}, apperr.ErrNotFound
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

func loc(p authz.Principal) *time.Location { return domain.Location(p.Timezone) }

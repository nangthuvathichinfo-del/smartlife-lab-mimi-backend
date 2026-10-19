// Package plan phục vụ Kế hoạch mua sắm (spec mục 5): Giỏ, thời gian mua, Nơi mua tối ưu.
//
//   - Mỗi nhóm hàng có tối đa một dòng giỏ đang mở: ID dòng = group_key (plan_items/{key}).
//   - Lưu hóa đơn có món trong giỏ thì dòng đó tự chuyển "done" (trong transaction lưu hóa đơn).
//   - Giá dùng để ước tính là giá người dùng từng trả, lấy từ hóa đơn đã lưu; không tìm giá ngoài.
//   - Giỏ, Giá tốt và ô Trang chủ cùng gọi domain.PlanShopping nên con số luôn khớp nhau.
package plan

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/item"
)

// Line là một dòng giỏ đang mở (plan_items/{key} với status "open").
type Line struct {
	Key         string
	Name        string
	CategoryKey string
	AddedAt     time.Time
}

// State là thông tin chuyến mua: ngày, giờ, ghi chú.
type State struct {
	BuyDate  string `json:"buy_date"`  // YYYY-MM-DD hoặc "" (chưa chọn)
	BuyHour  *int   `json:"buy_hour"`  // 0..23, null nếu chưa chọn
	TripNote string `json:"trip_note"` // tối đa 120 ký tự
}

type Store interface {
	OpenLines(ctx context.Context, uid string) ([]Line, error)
	// AddLine ghi đè dòng của nhóm thành "open" (kể cả khi trước đó đã "done").
	AddLine(ctx context.Context, uid string, l Line) error
	// RemoveLine xóa dòng; nhóm không có trong giỏ cũng không lỗi.
	RemoveLine(ctx context.Context, uid, key string) error
	State(ctx context.Context, uid string) (State, error)
	SetState(ctx context.Context, uid string, s State) error
	// Prices trả giá mỗi đơn vị gần nhất của từng nhóm tại từng cửa hàng người dùng từng mua.
	Prices(ctx context.Context, uid string, groups []string) ([]domain.LastPrice, error)
}

// Items là phần đọc của item.Store mà Kế hoạch cần.
type Items interface {
	GetItem(ctx context.Context, uid, key string) (item.Record, error)
	ListItems(ctx context.Context, uid string) ([]item.Record, error)
}

// MaxCartLines: giới hạn số món trong giỏ (một chuyến chợ thực tế vài chục món).
const MaxCartLines = 100

const maxTripNote = 120

// CartItem là một món trong Giỏ, kèm tồn hiện tại và giá rẻ nhất đã biết.
type CartItem struct {
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	CategoryKey string        `json:"category_key"`
	Status      domain.Status `json:"status"`
	DaysLeft    *int          `json:"days_left"`
	UnitPrice   *int64        `json:"unit_price"` // null: chưa có giá
	BestStore   string        `json:"best_store,omitempty"`
}

// Cart là phản hồi GET /plan.
type Cart struct {
	Items        []CartItem `json:"items"`
	ItemCount    int        `json:"item_count"`
	Estimate     *int64     `json:"estimate"`      // tổng theo phương án khuyên dùng; null khi chưa món nào có giá
	MissingPrice int        `json:"missing_price"` // số món chưa có giá, không cộng vào ước tính
	State        State      `json:"state"`
}

type Stop struct {
	Store  string   `json:"store"`
	Groups []string `json:"groups"`
	Total  int64    `json:"total"`
}

// Prices là phản hồi GET /plan/prices (schema PlanPrices).
type Prices struct {
	SingleStore  *Stop    `json:"single_store"`
	Split        []Stop   `json:"split"`
	SplitTotal   int64    `json:"split_total"`
	Saving       int64    `json:"saving"`
	Recommend    string   `json:"recommend"` // single | split
	MissingPrice []string `json:"missing_price"`
	SingleSource []string `json:"single_source"`
	SaleOffers   []any    `json:"sale_offers"` // chưa tìm giá sale bên ngoài: luôn rỗng
}

type Service struct {
	store Store
	items Items
	now   func() time.Time
}

func New(store Store, items Items, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, items: items, now: now}
}

// Get trả Giỏ: các dòng đang mở, tồn hiện tại của từng món, giá và ước tính.
func (s *Service) Get(ctx context.Context, p authz.Principal) (Cart, error) {
	if err := authz.Can(p, authz.ActionReadPlan); err != nil {
		return Cart{}, err
	}
	lines, err := s.store.OpenLines(ctx, p.UID)
	if err != nil {
		return Cart{}, err
	}
	state, err := s.store.State(ctx, p.UID)
	if err != nil {
		return Cart{}, err
	}
	cart := Cart{Items: []CartItem{}, State: state}
	if len(lines) == 0 {
		return cart, nil
	}
	recs, err := s.items.ListItems(ctx, p.UID)
	if err != nil {
		return Cart{}, err
	}
	byKey := make(map[string]item.Record, len(recs))
	for _, r := range recs {
		byKey[r.GroupKey] = r
	}
	sp, prices, err := s.plan(ctx, p.UID, lines)
	if err != nil {
		return Cart{}, err
	}
	today := domain.Today(s.now(), domain.Location(p.Timezone))
	for _, l := range lines {
		ci := CartItem{Key: l.Key, Name: l.Name, CategoryKey: l.CategoryKey, Status: domain.StatusWaiting}
		if r, ok := byKey[l.Key]; ok {
			v := domain.ViewAt(r.Prediction, today, leadDays(p))
			ci.Status, ci.DaysLeft = v.Status, v.DaysLeft
			if r.Name != "" {
				ci.Name = r.Name // tên mới nhất từ Đồ dùng
			}
		}
		if store, price, ok := cheapest(prices[l.Key]); ok {
			v := int64(price)
			ci.UnitPrice, ci.BestStore = &v, store
		}
		cart.Items = append(cart.Items, ci)
	}
	cart.ItemCount = len(cart.Items)
	cart.Estimate, cart.MissingPrice = estimate(sp)
	return cart, nil
}

// Summary là số phụ của ô Kế hoạch trên Trang chủ, cùng cách tính với Giỏ.
func (s *Service) Summary(ctx context.Context, p authz.Principal) (count int, est *int64, missing int, err error) {
	if err := authz.Can(p, authz.ActionReadPlan); err != nil {
		return 0, nil, 0, err
	}
	lines, err := s.store.OpenLines(ctx, p.UID)
	if err != nil || len(lines) == 0 {
		return 0, nil, 0, err
	}
	sp, _, err := s.plan(ctx, p.UID, lines)
	if err != nil {
		return 0, nil, 0, err
	}
	est, missing = estimate(sp)
	return len(lines), est, missing, nil
}

// Add cho một món trong Đồ dùng vào giỏ. Gọi lại nhiều lần vẫn chỉ có một dòng.
func (s *Service) Add(ctx context.Context, p authz.Principal, key string) (Cart, error) {
	if err := authz.Can(p, authz.ActionWritePlan); err != nil {
		return Cart{}, err
	}
	if !item.ValidKey(key) {
		return Cart{}, apperr.ErrNotFound
	}
	rec, err := s.items.GetItem(ctx, p.UID, key)
	if err != nil {
		return Cart{}, err // apperr.ErrNotFound khi món không có trong Đồ dùng
	}
	lines, err := s.store.OpenLines(ctx, p.UID)
	if err != nil {
		return Cart{}, err
	}
	already := false
	for _, l := range lines {
		if l.Key == key {
			already = true
			break
		}
	}
	if !already && len(lines) >= MaxCartLines {
		return Cart{}, apperr.New(apperr.CodeValidation, "Giỏ đã đủ 100 món.").With("limit", MaxCartLines)
	}
	if !already {
		if err := s.store.AddLine(ctx, p.UID, Line{Key: key, Name: rec.Name, CategoryKey: rec.CategoryKey, AddedAt: s.now()}); err != nil {
			return Cart{}, err
		}
	}
	return s.Get(ctx, p)
}

// Remove bỏ một món khỏi giỏ. Món không có trong giỏ vẫn coi là thành công.
func (s *Service) Remove(ctx context.Context, p authz.Principal, key string) error {
	if err := authz.Can(p, authz.ActionWritePlan); err != nil {
		return err
	}
	if !item.ValidKey(key) {
		return apperr.ErrNotFound
	}
	return s.store.RemoveLine(ctx, p.UID, key)
}

// SetState lưu ngày, giờ và ghi chú cho chuyến mua.
func (s *Service) SetState(ctx context.Context, p authz.Principal, st State) (State, error) {
	if err := authz.Can(p, authz.ActionWritePlan); err != nil {
		return State{}, err
	}
	st.TripNote = strings.TrimSpace(st.TripNote)
	if err := st.validate(domain.Today(s.now(), domain.Location(p.Timezone))); err != nil {
		return State{}, err
	}
	if err := s.store.SetState(ctx, p.UID, st); err != nil {
		return State{}, err
	}
	return st, nil
}

func (st State) validate(today domain.Date) error {
	bad := func(field, msg string) error {
		return apperr.New(apperr.CodeValidation, msg).With("field", field)
	}
	if st.BuyDate != "" {
		d, err := domain.ParseDate(st.BuyDate)
		if err != nil {
			return bad("buy_date", "Ngày mua không hợp lệ.")
		}
		// Cho phép chọn lùi vài ngày (ghi lại chuyến vừa đi) và tối đa một năm tới.
		if d.Before(today.AddDays(-7)) || d.After(today.AddDays(366)) {
			return bad("buy_date", "Ngày mua nằm ngoài khoảng cho phép.")
		}
	}
	if st.BuyHour != nil && (*st.BuyHour < 0 || *st.BuyHour > 23) {
		return bad("buy_hour", "Giờ mua từ 0 đến 23.")
	}
	if utf8.RuneCountInString(st.TripNote) > maxTripNote {
		return bad("trip_note", "Ghi chú tối đa 120 ký tự.")
	}
	if strings.IndexFunc(st.TripNote, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return bad("trip_note", "Ghi chú có ký tự không hợp lệ.")
	}
	return nil
}

// Prices trả Nơi mua tối ưu cho các món trong giỏ.
func (s *Service) Prices(ctx context.Context, p authz.Principal) (Prices, error) {
	if err := authz.Can(p, authz.ActionReadPlan); err != nil {
		return Prices{}, err
	}
	lines, err := s.store.OpenLines(ctx, p.UID)
	if err != nil {
		return Prices{}, err
	}
	sp, _, err := s.plan(ctx, p.UID, lines)
	if err != nil {
		return Prices{}, err
	}
	return toPrices(sp), nil
}

// plan đọc giá và gọi domain.PlanShopping (hàm duy nhất tính Nơi mua tối ưu).
func (s *Service) plan(ctx context.Context, uid string, lines []Line) (domain.ShoppingPlan, map[string]map[string]domain.VND, error) {
	groups := make([]string, 0, len(lines))
	for _, l := range lines {
		groups = append(groups, l.Key)
	}
	if len(groups) == 0 {
		return domain.PlanShopping(nil, nil), map[string]map[string]domain.VND{}, nil
	}
	lp, err := s.store.Prices(ctx, uid, groups)
	if err != nil {
		return domain.ShoppingPlan{}, nil, err
	}
	byGroup := map[string]map[string]domain.VND{}
	for _, x := range lp {
		if byGroup[x.GroupKey] == nil {
			byGroup[x.GroupKey] = map[string]domain.VND{}
		}
		byGroup[x.GroupKey][x.Store] = x.Unit
	}
	return domain.PlanShopping(groups, lp), byGroup, nil
}

// estimate: tổng theo phương án khuyên dùng. Món chưa có giá đếm riêng, không cộng.
func estimate(sp domain.ShoppingPlan) (*int64, int) {
	missing := len(sp.MissingPrice)
	var total domain.VND
	switch {
	case sp.Recommend == "single" && sp.SingleStore != nil:
		total = sp.SingleStore.Total
	case len(sp.Split) > 0:
		total = sp.SplitTotal
	default:
		return nil, missing
	}
	v := int64(total)
	return &v, missing
}

func toPrices(sp domain.ShoppingPlan) Prices {
	out := Prices{
		Split: []Stop{}, SplitTotal: int64(sp.SplitTotal), Saving: int64(sp.Saving), Recommend: sp.Recommend,
		MissingPrice: nonNil(sp.MissingPrice), SingleSource: nonNil(sp.SingleSource), SaleOffers: []any{},
	}
	if out.Saving < 0 {
		out.Saving = 0
	}
	if sp.SingleStore != nil {
		out.SingleStore = &Stop{Store: sp.SingleStore.Store, Groups: nonNil(sp.SingleStore.Groups), Total: int64(sp.SingleStore.Total)}
	}
	for _, st := range sp.Split {
		g := append([]string(nil), st.Groups...)
		sort.Strings(g)
		out.Split = append(out.Split, Stop{Store: st.Store, Groups: g, Total: int64(st.Total)})
	}
	return out
}

func cheapest(m map[string]domain.VND) (string, domain.VND, bool) {
	var best string
	var price domain.VND = -1
	for st, p := range m {
		if price < 0 || p < price || (p == price && st < best) {
			best, price = st, p
		}
	}
	return best, price, price >= 0
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func leadDays(p authz.Principal) int {
	if p.LeadDays >= 1 && p.LeadDays <= 30 {
		return p.LeadDays
	}
	return 7
}

package fsstore

import (
	"context"
	"fmt"
	"math"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
)

var _ plan.Store = (*Store)(nil)

// Giỏ: users/{uid}/plan_items/{group_key} {status: open|done, name, category_key, added_at, src}.
// Trạng thái chuyến mua: trường plan_state trên users/{uid}.

const (
	maxPlanLines = 200
	// pricesPerGroup: số lần mua gần nhất đọc cho mỗi nhóm để lấy giá mới nhất ở từng cửa hàng.
	pricesPerGroup = 20
)

func (s *Store) OpenLines(ctx context.Context, uid string) ([]plan.Line, error) {
	docs, err := s.user(uid).Collection("plan_items").Where("status", "==", "open").
		Limit(maxPlanLines).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("plan lines: %w", err)
	}
	out := make([]plan.Line, 0, len(docs))
	for _, d := range docs {
		m := d.Data()
		l := plan.Line{Key: d.Ref.ID}
		l.Name, _ = m["name"].(string)
		l.CategoryKey, _ = m["category_key"].(string)
		l.AddedAt, _ = m["added_at"].(time.Time)
		out = append(out, l)
	}
	return out, nil
}

// AddLine ghi đè cả tài liệu (không merge) để xóa dấu "done" của lần mua trước.
func (s *Store) AddLine(ctx context.Context, uid string, l plan.Line) error {
	_, err := s.user(uid).Collection("plan_items").Doc(l.Key).Set(ctx, map[string]any{
		"status": "open", "name": l.Name, "category_key": l.CategoryKey, "added_at": l.AddedAt, "src": "manual",
	})
	if err != nil {
		return fmt.Errorf("plan add: %w", err)
	}
	return nil
}

// RemoveLine: xóa tài liệu không tồn tại không phải lỗi trong Firestore.
func (s *Store) RemoveLine(ctx context.Context, uid, key string) error {
	if _, err := s.user(uid).Collection("plan_items").Doc(key).Delete(ctx); err != nil {
		return fmt.Errorf("plan remove: %w", err)
	}
	return nil
}

func (s *Store) State(ctx context.Context, uid string) (plan.State, error) {
	snap, err := s.user(uid).Get(ctx)
	if isNotFound(err) {
		return plan.State{}, nil
	}
	if err != nil {
		return plan.State{}, fmt.Errorf("plan state: %w", err)
	}
	m, _ := snap.Data()["plan_state"].(map[string]any)
	var st plan.State
	st.BuyDate, _ = m["buy_date"].(string)
	st.TripNote, _ = m["trip_note"].(string)
	if h, ok := m["buy_hour"].(int64); ok {
		hh := int(h)
		st.BuyHour = &hh
	}
	return st, nil
}

func (s *Store) SetState(ctx context.Context, uid string, st plan.State) error {
	var hour any // nil: chưa chọn giờ
	if st.BuyHour != nil {
		hour = int64(*st.BuyHour)
	}
	_, err := s.user(uid).Update(ctx, []firestore.Update{{Path: "plan_state", Value: map[string]any{
		"buy_date": st.BuyDate, "buy_hour": hour, "trip_note": st.TripNote, "updated_at": time.Now(),
	}}})
	if err != nil {
		return fmt.Errorf("plan set state: %w", err)
	}
	return nil
}

// Prices đọc các lần mua gần nhất của từng nhóm (index purchases group_key + habit +
// purchased_on DESC có sẵn) và giữ giá mới nhất ở mỗi cửa hàng. Giá mỗi đơn vị =
// thành tiền dòng / số lượng. Dòng chưa quy đổi được sang VND hoặc không rõ cửa hàng bị bỏ qua.
// Giới hạn: chỉ xét dòng đánh dấu "dùng để dự đoán" (habit), vì index chỉ phủ trường hợp đó.
func (s *Store) Prices(ctx context.Context, uid string, groups []string) ([]domain.LastPrice, error) {
	var out []domain.LastPrice
	for _, g := range groups {
		docs, err := s.user(uid).Collection("purchases").
			Where("group_key", "==", g).Where("habit", "==", true).
			OrderBy("purchased_on", firestore.Desc).Limit(pricesPerGroup).Documents(ctx).GetAll()
		if err != nil {
			return nil, fmt.Errorf("plan prices %s: %w", g, err)
		}
		seen := map[string]bool{}
		for _, d := range docs {
			m := d.Data()
			store, _ := m["store"].(string)
			total, ok := m["line_total_vnd"].(int64)
			qty := asFloat(m["qty"])
			if store == "" || seen[store] || !ok || total <= 0 || qty <= 0 {
				continue
			}
			seen[store] = true // tài liệu đã xếp mới nhất trước: giá đầu tiên gặp là giá mới nhất
			on, _ := domain.ParseDate(fmt.Sprint(m["purchased_on"]))
			out = append(out, domain.LastPrice{
				GroupKey: g, Store: store, Unit: domain.VND(math.Round(float64(total) / qty)), On: on,
			})
		}
	}
	return out, nil
}

// asFloat: qty lưu dạng số thực, nhưng giá trị nguyên có thể được đọc về dạng int64.
func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}

package fsstore

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/home"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/spending"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

// Đọc cho Trang chủ, Chi tiết món, Sổ chi tiêu và ghi Cài đặt.
// Không có truy vấn nào cần index ghép mới:
//   - purchases(group_key, habit, purchased_on DESC) đã có trong firestore.indexes.json;
//   - receipts lọc khoảng trên một trường (purchased_on) dùng index đơn trường tự tạo.

var (
	_ home.Store         = (*Store)(nil)
	_ spending.Store     = (*Store)(nil)
	_ user.SettingsStore = (*Store)(nil)
)

// History đọc các lần mua thói quen gần nhất của một nhóm, mới nhất trước.
func (s *Store) History(ctx context.Context, uid, key string, limit int) ([]item.HistoryEntry, error) {
	docs, err := s.user(uid).Collection("purchases").
		Where("group_key", "==", key).Where("habit", "==", true).
		OrderBy("purchased_on", firestore.Desc).Limit(limit).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("item history: %w", err)
	}
	out := make([]item.HistoryEntry, 0, len(docs))
	for _, d := range docs {
		m := d.Data()
		e := item.HistoryEntry{}
		e.On, _ = m["purchased_on"].(string)
		e.Store, _ = m["store"].(string)
		e.ReceiptID, _ = m["receipt_id"].(string)
		if v, ok := m["line_total_vnd"].(int64); ok {
			e.AmountVND = &v
		}
		out = append(out, e)
	}
	return out, nil
}

// HomeRaw đọc users/{uid} và months/{month}. Số liệu giỏ lấy từ plan.Service.Summary.
func (s *Store) HomeRaw(ctx context.Context, uid, month string) (home.Raw, error) {
	u := s.user(uid)
	var raw home.Raw
	snap, err := u.Get(ctx)
	switch {
	case err == nil:
		raw.ReceiptCount = int(asInt64(snap.Data()["receipt_count"]))
	case !isNotFound(err):
		return home.Raw{}, fmt.Errorf("home user: %w", err)
	}
	m, err := s.Month(ctx, uid, month)
	if err != nil {
		return home.Raw{}, err
	}
	raw.SpentVND, raw.MonthHasData = m.SpentVND, m.Receipts > 0
	return raw, nil
}

// Month đọc months/{month}. Tháng chưa có hóa đơn trả giá trị rỗng, không phải lỗi.
func (s *Store) Month(ctx context.Context, uid, month string) (spending.Month, error) {
	snap, err := s.user(uid).Collection("months").Doc(month).Get(ctx)
	if isNotFound(err) {
		return spending.Month{}, nil
	}
	if err != nil {
		return spending.Month{}, fmt.Errorf("get month: %w", err)
	}
	d := snap.Data()
	m := spending.Month{
		SpentVND:    asInt64(d["spent_vnd"]),
		DiscountVND: asInt64(d["discount_vnd"]),
		Receipts:    asInt64(d["receipts"]),
		ByCategory:  map[string]int64{},
	}
	if cats, ok := d["by_category"].(map[string]any); ok {
		for k, v := range cats {
			m.ByCategory[k] = asInt64(v)
		}
	}
	return m, nil
}

// MonthReceipts lọc receipts theo purchased_on ("YYYY-MM-DD", so sánh chuỗi đúng thứ tự ngày).
func (s *Store) MonthReceipts(ctx context.Context, uid, month string, limit int) ([]spending.ReceiptRow, error) {
	docs, err := s.user(uid).Collection("receipts").
		Where("purchased_on", ">=", month+"-01").
		Where("purchased_on", "<=", month+"-31").
		Limit(limit).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("month receipts: %w", err)
	}
	out := make([]spending.ReceiptRow, 0, len(docs))
	for _, d := range docs {
		m := d.Data()
		r := spending.ReceiptRow{ID: d.Ref.ID, TotalVND: asInt64(m["total_vnd"]), DiscountVND: asInt64(m["discount_vnd"])}
		r.Store, _ = m["store"].(string)
		r.PurchasedOn, _ = m["purchased_on"].(string)
		out = append(out, r)
	}
	return out, nil
}

// UpdateSettings đọc rồi ghi trong một transaction để hai lần lưu gần nhau không đè nhau.
// Tài liệu users/{uid} luôn có sẵn vì LoadPrincipal tạo nó ở request đầu tiên.
func (s *Store) UpdateSettings(ctx context.Context, uid string, apply func(user.Stored) (user.Stored, error)) (user.Stored, error) {
	ref := s.user(uid)
	var out user.Stored
	err := s.c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if isNotFound(err) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		d := snap.Data()
		cur := user.Stored{}
		cur.DisplayName, _ = d["display_name"].(string)
		cur.Settings, _ = d["settings"].(map[string]any)
		next, err := apply(cur)
		if err != nil {
			return err
		}
		ups := []firestore.Update{
			{Path: "display_name", Value: next.DisplayName},
			{Path: "settings", Value: next.Settings},
			{Path: "updated_at", Value: time.Now()},
		}
		if next.NextNotifyAt != nil {
			ups = append(ups, firestore.Update{Path: "next_notify_at", Value: *next.NextNotifyAt})
		}
		out = next
		return tx.Update(ref, ups)
	})
	if err != nil {
		return user.Stored{}, err
	}
	return out, nil
}

// asInt64: Firestore trả số nguyên dạng int64; số thực (nếu ai đó ghi tay) thì cắt phần lẻ.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

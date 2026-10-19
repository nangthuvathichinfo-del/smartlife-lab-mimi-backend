package item

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

type memStore struct {
	recs map[string]map[string]Record
	hist map[string][]HistoryEntry // group_key -> lịch sử, mới nhất trước
}

func (m *memStore) History(_ context.Context, _ string, k string, limit int) ([]HistoryEntry, error) {
	h := m.hist[k]
	if len(h) > limit {
		h = h[:limit]
	}
	return h, nil
}

func (m *memStore) ListItems(_ context.Context, uid string) ([]Record, error) {
	var out []Record
	for _, r := range m.recs[uid] {
		out = append(out, r)
	}
	return out, nil
}
func (m *memStore) GetItem(_ context.Context, uid, k string) (Record, error) {
	r, ok := m.recs[uid][k]
	if !ok {
		return Record{}, apperr.ErrNotFound
	}
	return r, nil
}
func (m *memStore) SetOverride(_ context.Context, uid, k string, ov domain.Override) (Record, error) {
	r, ok := m.recs[uid][k]
	if !ok {
		return Record{}, apperr.ErrNotFound
	}
	if r.Override == nil || ov.SetAt.After(r.Override.SetAt) {
		r.Override = &ov
	}
	r.Prediction = domain.Predict([]domain.Purchase{{On: r.LastPurchase, Habit: true}}, r.Override)
	m.recs[uid][k] = r
	return r, nil
}
func (m *memStore) ClearOverride(_ context.Context, uid, k string, _ time.Time) (Record, error) {
	r := m.recs[uid][k]
	r.Override = nil
	m.recs[uid][k] = r
	return r, nil
}
func (m *memStore) OwnerOf(context.Context, string) (string, error) { return "owner", nil }

func date(s string) domain.Date { d, _ := domain.ParseDate(s); return d }

func TestItems(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC) // 10h sáng giờ VN
	st := &memStore{recs: map[string]map[string]Record{"owner": {
		"sua":   {GroupKey: "sua", Name: "Sữa tươi", NameNorm: "sua tuoi", Prediction: domain.Prediction{OutOn: date("2026-10-16"), PurchaseCount: 5, Source: "predicted"}, LastPurchase: date("2026-10-01")},
		"gao":   {GroupKey: "gao", Name: "Gạo", NameNorm: "gao", Prediction: domain.Prediction{OutOn: date("2026-11-20"), PurchaseCount: 4, Source: "predicted"}, LastPurchase: date("2026-10-01")},
		"trung": {GroupKey: "trung", Name: "Trứng gà", NameNorm: "trung ga", Prediction: domain.Prediction{OutOn: date("2026-10-19"), PurchaseCount: 3, Source: "predicted"}, LastPurchase: date("2026-10-01")},
	}}}
	svc := New(st, func() time.Time { return now })
	owner := authz.Principal{UID: "owner", Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh", LeadDays: 7}
	member := authz.Principal{UID: "m1", Role: authz.RoleMember, HouseholdID: "h1"}

	t.Run("soon tab sorted by days", func(t *testing.T) {
		got, err := svc.List(context.Background(), owner, Query{Tab: "soon", Horizon: 7})
		if err != nil || len(got) != 2 || got[0].Key != "sua" || got[0].Status != domain.StatusBuyNow {
			t.Fatalf("err=%v got=%+v", err, got)
		}
	})
	t.Run("accent-insensitive search", func(t *testing.T) {
		got, _ := svc.List(context.Background(), owner, Query{Tab: "active", Horizon: 7, Search: "TRUNG"})
		if len(got) != 1 || got[0].Key != "trung" {
			t.Fatalf("got=%+v", got)
		}
	})
	t.Run("member gets shared view, not full list", func(t *testing.T) {
		if _, err := svc.List(context.Background(), member, Query{Tab: "soon", Horizon: 7}); !errors.Is(err, apperr.ErrForbidden) {
			t.Fatalf("err=%v", err)
		}
		shared, err := svc.ListShared(context.Background(), member, 7)
		if err != nil || len(shared) != 2 {
			t.Fatalf("err=%v shared=%+v", err, shared)
		}
	})
	t.Run("stale offline change does not overwrite newer one", func(t *testing.T) {
		newer := now.Add(-1 * time.Minute)
		older := now.Add(-10 * time.Minute)
		if _, err := svc.ChangeStock(context.Background(), owner, "gao", StockChange{Quick: "out", ActedAt: newer}); err != nil {
			t.Fatal(err)
		}
		it, err := svc.ChangeStock(context.Background(), owner, "gao", StockChange{Quick: "plenty", ActedAt: older})
		if err != nil || it.Status != domain.StatusOut {
			t.Fatalf("err=%v item=%+v", err, it)
		}
	})
	t.Run("rejects invalid input", func(t *testing.T) {
		bad := 61
		cases := []StockChange{
			{Quick: "lots", ActedAt: now},
			{Days: &bad, ActedAt: now},
			{Quick: "out", ActedAt: now.Add(time.Hour)}, // tương lai
			{Quick: "out"},                              // thiếu acted_at
		}
		for i, c := range cases {
			var ae *apperr.Error
			if _, err := svc.ChangeStock(context.Background(), owner, "gao", c); !errors.As(err, &ae) || ae.Code != apperr.CodeValidation {
				t.Errorf("case %d: err=%v", i, err)
			}
		}
	})
}

func TestDetailAndNeedsCount(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	amt := int64(32000)
	st := &memStore{
		recs: map[string]map[string]Record{"owner": {
			"sua":   {GroupKey: "sua", Name: "Sữa tươi", Prediction: domain.Prediction{OutOn: date("2026-10-16"), PurchaseCount: 5, Source: "predicted"}},
			"gao":   {GroupKey: "gao", Name: "Gạo", Prediction: domain.Prediction{OutOn: date("2026-11-20"), PurchaseCount: 4, Source: "predicted"}},
			"bot":   {GroupKey: "bot", Name: "Bột giặt", Prediction: domain.Prediction{OutOn: date("2026-10-18"), PurchaseCount: 3, Source: "predicted"}, Dropped: true},
			"keo":   {GroupKey: "keo", Name: "Kẹo", Prediction: domain.Prediction{OutOn: date("2026-10-17"), PurchaseCount: 1, Source: "predicted"}}, // chưa đủ dữ liệu
			"trung": {GroupKey: "trung", Name: "Trứng", Prediction: domain.Prediction{OutOn: date("2026-10-26"), PurchaseCount: 3, Source: "predicted"}},
		}},
		hist: map[string][]HistoryEntry{"sua": {{On: "2026-10-01", Store: "Co.op", AmountVND: &amt}, {On: "2026-09-20"}}},
	}
	svc := New(st, func() time.Time { return now })
	owner := authz.Principal{UID: "owner", Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh", LeadDays: 7}
	member := authz.Principal{UID: "m1", Role: authz.RoleMember, HouseholdID: "h1"}
	ctx := context.Background()

	t.Run("detail has item fields and history", func(t *testing.T) {
		d, err := svc.Get(ctx, owner, "sua")
		if err != nil || d.Key != "sua" || d.Status != domain.StatusBuyNow || len(d.History) != 2 || *d.History[0].AmountVND != 32000 {
			t.Fatalf("err=%v d=%+v", err, d)
		}
	})
	t.Run("detail without history is empty list, not nil", func(t *testing.T) {
		d, err := svc.Get(ctx, owner, "gao")
		if err != nil || d.History == nil || len(d.History) != 0 {
			t.Fatalf("err=%v hist=%v", err, d.History)
		}
	})
	t.Run("unknown or invalid key is 404", func(t *testing.T) {
		for _, k := range []string{"khong-co", "", "..", "__x__", "a/b"} {
			if _, err := svc.Get(ctx, owner, k); !errors.Is(err, apperr.ErrNotFound) {
				t.Errorf("key %q: err=%v", k, err)
			}
		}
	})
	t.Run("member cannot read detail", func(t *testing.T) {
		if _, err := svc.Get(ctx, member, "sua"); !errors.Is(err, apperr.ErrForbidden) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("needs count skips dropped and waiting items", func(t *testing.T) {
		// sua: hết hôm nay; trung: còn 10 ngày (ngoài 7); bot: đã bỏ; keo: chưa đủ dữ liệu.
		n, err := svc.CountNeeds(ctx, owner, 7)
		if err != nil || n != 1 {
			t.Fatalf("err=%v n=%d", err, n)
		}
		if n, _ := svc.CountNeeds(ctx, owner, 14); n != 2 {
			t.Fatalf("horizon 14: n=%d", n)
		}
	})
	t.Run("member count uses shared list", func(t *testing.T) {
		if n, err := svc.CountNeeds(ctx, member, 7); err != nil || n != 1 {
			t.Fatalf("err=%v n=%d", err, n)
		}
	})
}

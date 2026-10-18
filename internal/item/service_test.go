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

type memStore struct{ recs map[string]map[string]Record }

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

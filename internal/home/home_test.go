package home

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
)

type fakeStore struct {
	raw      Raw
	gotMonth string
	gotUID   string
	failWith error
}

func (f *fakeStore) HomeRaw(_ context.Context, uid, month string) (Raw, error) {
	f.gotUID, f.gotMonth = uid, month
	return f.raw, f.failWith
}

type fakePlan struct {
	n       int
	est     *int64
	missing int
	called  bool
}

func (f *fakePlan) Summary(context.Context, authz.Principal) (int, *int64, int, error) {
	f.called = true
	return f.n, f.est, f.missing, nil
}

type fakeNeeds struct {
	n       int
	horizon int
}

func (f *fakeNeeds) CountNeeds(_ context.Context, _ authz.Principal, h int) (int, error) {
	f.horizon = h
	return f.n, nil
}

func TestHome(t *testing.T) {
	// 31/10 lúc 20h giờ UTC là 1/11 lúc 3h sáng giờ Việt Nam: tháng phải là 2026-11.
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	owner := authz.Principal{UID: "u1", Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh"}
	member := authz.Principal{UID: "u2", Role: authz.RoleMember, HouseholdID: "h1"}
	ctx := context.Background()

	t.Run("owner sees spending, needs and plan", func(t *testing.T) {
		st := &fakeStore{raw: Raw{SpentVND: 1284000, MonthHasData: true, ReceiptCount: 4}}
		nd := &fakeNeeds{n: 5}
		est := int64(186000)
		got, err := New(st, nd, &fakePlan{n: 3, est: &est, missing: 1}, func() time.Time { return now }).Get(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		if st.gotMonth != "2026-11" || st.gotUID != "u1" || nd.horizon != 7 {
			t.Fatalf("month=%s uid=%s horizon=%d", st.gotMonth, st.gotUID, nd.horizon)
		}
		if got.SpentThisMonth == nil || *got.SpentThisMonth != 1284000 || got.NeedsCount != 5 ||
			got.Plan.ItemCount != 3 || got.Plan.Estimate == nil || *got.Plan.Estimate != 186000 || got.Plan.MissingPrice != 1 || got.ReceiptCount != 4 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("month without receipts gives null spending", func(t *testing.T) {
		got, err := New(&fakeStore{raw: Raw{}}, &fakeNeeds{}, &fakePlan{}, func() time.Time { return now }).Get(ctx, owner)
		if err != nil || got.SpentThisMonth != nil {
			t.Fatalf("err=%v got=%+v", err, got)
		}
	})
	t.Run("member never sees spending or plan", func(t *testing.T) {
		st := &fakeStore{raw: Raw{SpentVND: 999, MonthHasData: true}}
		pl := &fakePlan{n: 2}
		got, err := New(st, &fakeNeeds{n: 2}, pl, func() time.Time { return now }).Get(ctx, member)
		if err != nil || got.SpentThisMonth != nil || got.Plan.ItemCount != 0 || got.NeedsCount != 2 || pl.called {
			t.Fatalf("err=%v got=%+v", err, got)
		}
	})
	t.Run("unauthenticated and store errors propagate", func(t *testing.T) {
		svc := New(&fakeStore{}, &fakeNeeds{}, &fakePlan{}, nil)
		if _, err := svc.Get(ctx, authz.Principal{}); !errors.Is(err, apperr.ErrUnauthenticated) {
			t.Fatalf("err=%v", err)
		}
		boom := errors.New("boom")
		if _, err := New(&fakeStore{failWith: boom}, &fakeNeeds{}, &fakePlan{}, nil).Get(ctx, owner); !errors.Is(err, boom) {
			t.Fatalf("err=%v", err)
		}
	})
}

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
)

type fakeItems struct{ gotHorizon int }

func (f *fakeItems) List(_ context.Context, _ authz.Principal, q item.Query) ([]item.Item, error) {
	f.gotHorizon = q.Horizon
	zero := 0
	return []item.Item{{Key: "sua", Name: "Sữa", Status: domain.StatusBuyNow, DaysLeft: &zero}}, nil
}
func (f *fakeItems) ListShared(context.Context, authz.Principal, int) ([]item.SharedItem, error) {
	return []item.SharedItem{{Key: "gao", Name: "Gạo", Status: domain.StatusSoon}}, nil
}

type fakePlans struct{ cart plan.Cart }

func (f fakePlans) Get(context.Context, authz.Principal) (plan.Cart, error) { return f.cart, nil }

func TestList(t *testing.T) {
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC) // 10h sáng 18/10 giờ VN
	owner := authz.Principal{UID: "u1", Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh"}
	ctx := context.Background()
	nine := 9

	t.Run("trip tomorrow comes first, then items", func(t *testing.T) {
		it := &fakeItems{}
		cart := plan.Cart{ItemCount: 3, State: plan.State{BuyDate: "2026-10-19", BuyHour: &nine}}
		got, err := New(it, fakePlans{cart}, func() time.Time { return now }).List(ctx, owner)
		if err != nil || len(got.Items) != 2 || got.Items[0].Kind != "trip" || got.Items[0].ItemCount != 3 || got.Items[1].ItemKey != "sua" {
			t.Fatalf("err=%v got=%+v", err, got)
		}
		if it.gotHorizon != 7 {
			t.Fatalf("horizon %d", it.gotHorizon)
		}
	})
	t.Run("no trip when the date is far, past, or the cart is empty", func(t *testing.T) {
		for _, c := range []plan.Cart{
			{ItemCount: 3, State: plan.State{BuyDate: "2026-10-25"}},
			{ItemCount: 3, State: plan.State{BuyDate: "2026-10-17"}},
			{ItemCount: 0, State: plan.State{BuyDate: "2026-10-18"}},
		} {
			got, _ := New(&fakeItems{}, fakePlans{c}, func() time.Time { return now }).List(ctx, owner)
			if len(got.Items) != 1 || got.Items[0].Kind != "item" {
				t.Errorf("cart %+v got %+v", c, got)
			}
		}
	})
	t.Run("member sees shared items only, never the owner's trip", func(t *testing.T) {
		member := authz.Principal{UID: "u2", Role: authz.RoleMember, HouseholdID: "h1"}
		got, err := New(&fakeItems{}, fakePlans{plan.Cart{ItemCount: 3, State: plan.State{BuyDate: "2026-10-18"}}}, nil).List(ctx, member)
		if err != nil || len(got.Items) != 1 || got.Items[0].ItemKey != "gao" {
			t.Fatalf("err=%v got=%+v", err, got)
		}
	})
	t.Run("empty list encodes as []", func(t *testing.T) {
		b, _ := json.Marshal(List{Items: []Note{}})
		if !strings.Contains(string(b), `"items":[]`) {
			t.Fatal(string(b))
		}
	})
	t.Run("unauthenticated", func(t *testing.T) {
		if _, err := New(&fakeItems{}, fakePlans{}, nil).List(ctx, authz.Principal{}); !errors.Is(err, apperr.ErrUnauthenticated) {
			t.Fatalf("err=%v", err)
		}
	})
}

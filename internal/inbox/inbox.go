// Package inbox dựng danh sách Thông báo (GET /notifications).
//
// Thông báo được suy ra lúc đọc từ dữ liệu sẵn có, không lưu thành bản ghi riêng:
//   - món trong "Cần mua" 7 ngày (cùng quy tắc với Đồ dùng và ô Trang chủ);
//   - chuyến mua đã hẹn hôm nay hoặc ngày mai khi giỏ còn món (chỉ chủ giỏ).
//
// Vì không lưu, chưa có trạng thái "đã đọc"; nội dung luôn phản ánh tình trạng hiện tại.
package inbox

import (
	"context"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
)

// Items là phần của item.Service mà Thông báo cần.
type Items interface {
	List(ctx context.Context, p authz.Principal, q item.Query) ([]item.Item, error)
	ListShared(ctx context.Context, p authz.Principal, horizon int) ([]item.SharedItem, error)
}

// Plans là phần của plan.Service mà Thông báo cần.
type Plans interface {
	Get(ctx context.Context, p authz.Principal) (plan.Cart, error)
}

// Note là một thông báo. Client dựng câu chữ theo Kind và ngôn ngữ giao diện.
type Note struct {
	ID        string        `json:"id"`   // ổn định giữa các lần gọi, dùng làm key khi hiển thị
	Kind      string        `json:"kind"` // item | trip
	ItemKey   string        `json:"item_key,omitempty"`
	Name      string        `json:"name,omitempty"`
	Status    domain.Status `json:"status,omitempty"`
	DaysLeft  *int          `json:"days_left,omitempty"`
	Date      string        `json:"date,omitempty"` // trip: YYYY-MM-DD
	Hour      *int          `json:"hour,omitempty"` // trip: 0..23
	ItemCount int           `json:"item_count,omitempty"`
}

type List struct {
	Items []Note `json:"items"`
}

// horizon: khoảng ngày của "Cần mua" mặc định.
const horizon = item.NeedsHorizonDays

type Service struct {
	items Items
	plans Plans
	now   func() time.Time
}

func New(items Items, plans Plans, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{items: items, plans: plans, now: now}
}

func (s *Service) List(ctx context.Context, p authz.Principal) (List, error) {
	if err := authz.Can(p, authz.ActionReadSelf); err != nil {
		return List{}, err
	}
	out := List{Items: []Note{}}

	if p.Role == authz.RoleMember {
		shared, err := s.items.ListShared(ctx, p, horizon)
		if err != nil {
			return List{}, err
		}
		for _, i := range shared {
			out.Items = append(out.Items, Note{ID: "item:" + i.Key, Kind: "item", ItemKey: i.Key, Name: i.Name, Status: i.Status, DaysLeft: i.DaysLeft})
		}
		return out, nil
	}

	// Chủ giỏ: chuyến mua sắp tới đứng đầu, sau đó là các món, hết sớm nhất trước.
	cart, err := s.plans.Get(ctx, p)
	if err != nil {
		return List{}, err
	}
	if cart.ItemCount > 0 && cart.State.BuyDate != "" {
		today := domain.Today(s.now(), domain.Location(p.Timezone))
		if d, err := domain.ParseDate(cart.State.BuyDate); err == nil && (d.Equal(today) || d.Equal(today.AddDays(1))) {
			out.Items = append(out.Items, Note{
				ID: "trip:" + cart.State.BuyDate, Kind: "trip", Date: cart.State.BuyDate,
				Hour: cart.State.BuyHour, ItemCount: cart.ItemCount,
			})
		}
	}
	items, err := s.items.List(ctx, p, item.Query{Tab: "soon", Horizon: horizon})
	if err != nil {
		return List{}, err
	}
	for _, i := range items {
		out.Items = append(out.Items, Note{ID: "item:" + i.Key, Kind: "item", ItemKey: i.Key, Name: i.Name, Status: i.Status, DaysLeft: i.DaysLeft})
	}
	return out, nil
}

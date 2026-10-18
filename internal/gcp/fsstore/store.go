// Package fsstore hiện thực các interface lưu trữ bằng Firestore (Native mode).
//
// Quy tắc của package:
//   - Mọi đường dẫn bắt đầu bằng users/{uid} với uid lấy từ Principal, không từ request.
//   - Quy tắc "duy nhất" được bảo đảm bằng ID tài liệu cố định (receipt_id, group_key,
//     ngày quota, ngày digest), vì Firestore không có ràng buộc UNIQUE.
//   - Ghi nhiều tài liệu liên quan luôn nằm trong một transaction; trong transaction
//     mọi thao tác đọc phải đứng trước mọi thao tác ghi.
//   - Tài liệu tạm (drafts, quota) có trường expire_at để TTL policy tự xóa.
//
// Lưu ý: package này chưa được biên dịch trong môi trường soạn khung (không tải được
// module). Chạy go mod tidy và test trên Firestore emulator trước khi dùng.
package fsstore

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

type Store struct {
	c *firestore.Client
}

// New nhận client đã tạo sẵn (cmd/mimi tạo, đóng khi tắt). Khi FIRESTORE_EMULATOR_HOST
// được đặt, SDK tự trỏ vào emulator.
func New(c *firestore.Client) *Store { return &Store{c: c} }

func (s *Store) user(uid string) *firestore.DocumentRef { return s.c.Collection("users").Doc(uid) }

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

// ---------- Sẵn sàng ----------

func (s *Store) Ready(ctx context.Context) error {
	_, err := s.c.Collection("_health").Doc("ping").Get(ctx)
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// ---------- Người dùng ----------

type userDoc struct {
	DisplayName  string         `firestore:"display_name"`
	HouseholdID  string         `firestore:"household_id"`
	Role         string         `firestore:"role"` // owner | member
	ReceiptCount int            `firestore:"receipt_count"`
	Settings     map[string]any `firestore:"settings"`
	Deleting     bool           `firestore:"deleting"`
}

// LoadPrincipal đọc users/{uid}; lần đăng nhập đầu tiên thì tạo tài liệu mặc định
// (người dùng một mình là chủ của nhà một người).
func (s *Store) LoadPrincipal(ctx context.Context, uid string) (authz.Principal, error) {
	ref := s.user(uid)
	snap, err := ref.Get(ctx)
	if isNotFound(err) {
		doc := map[string]any{
			"role": "owner", "receipt_count": 0, "created_at": firestore.ServerTimestamp,
			"settings": map[string]any{"lead_days": 7, "timezone": "Asia/Ho_Chi_Minh", "lang": "vi", "remind_enabled": true},
		}
		if _, err := ref.Create(ctx, doc); err != nil && status.Code(err) != codes.AlreadyExists {
			return authz.Principal{}, fmt.Errorf("create user: %w", err)
		}
		return authz.Principal{UID: uid, Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh", LeadDays: 7}, nil
	}
	if err != nil {
		return authz.Principal{}, fmt.Errorf("get user: %w", err)
	}
	var ud userDoc
	if err := snap.DataTo(&ud); err != nil {
		return authz.Principal{}, fmt.Errorf("decode user: %w", err)
	}
	if ud.Deleting {
		return authz.Principal{}, apperr.ErrUnauthenticated
	}
	p := authz.Principal{UID: uid, HouseholdID: ud.HouseholdID, Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh", LeadDays: 7}
	if ud.Role == "member" {
		p.Role = authz.RoleMember
	}
	if tz, ok := ud.Settings["timezone"].(string); ok && tz != "" {
		p.Timezone = tz
	}
	if ld, ok := ud.Settings["lead_days"].(int64); ok {
		p.LeadDays = int(ld)
	}
	return p, nil
}

func (s *Store) Profile(ctx context.Context, p authz.Principal) (user.Profile, error) {
	snap, err := s.user(p.UID).Get(ctx)
	if err != nil {
		return user.Profile{}, fmt.Errorf("get profile: %w", err)
	}
	var ud userDoc
	if err := snap.DataTo(&ud); err != nil {
		return user.Profile{}, err
	}
	return user.Profile{
		UID: p.UID, DisplayName: ud.DisplayName, Role: p.Role.String(),
		HouseholdID: ud.HouseholdID, ReceiptCount: ud.ReceiptCount, Settings: ud.Settings,
	}, nil
}

// ---------- Quota đọc hóa đơn ----------

// Reserve tăng quota/{day} trong transaction: nhiều instance cùng chạy vẫn đếm đúng.
func (s *Store) Reserve(ctx context.Context, uid, day string, limit int) error {
	ref := s.user(uid).Collection("quota").Doc(day)
	return s.c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		used := int64(0)
		snap, err := tx.Get(ref)
		switch {
		case err == nil:
			if v, ok := snap.Data()["used"].(int64); ok {
				used = v
			}
		case !isNotFound(err):
			return err
		}
		if used >= int64(limit) {
			return apperr.New(apperr.CodeQuotaExceeded, fmt.Sprintf("Hôm nay bạn đã đọc %d hóa đơn, mai đọc tiếp nhé.", limit)).
				With("limit", limit)
		}
		return tx.Set(ref, map[string]any{
			"used":      used + 1,
			"expire_at": time.Now().Add(48 * time.Hour), // TTL policy xóa
		}, firestore.MergeAll)
	})
}

func (s *Store) Release(ctx context.Context, uid, day string) error {
	_, err := s.user(uid).Collection("quota").Doc(day).Update(ctx, []firestore.Update{
		{Path: "used", Value: firestore.Increment(-1)},
	})
	return err
}

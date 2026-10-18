package fsstore

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/notify"
)

// Emails lấy email đăng nhập từ Firebase Auth (email không lưu ở Firestore).
type Emails interface {
	Email(ctx context.Context, uid string) (string, error)
}

// NotifyStore hiện thực notify.Store.
type NotifyStore struct {
	*Store
	emails Emails
}

func NewNotifyStore(s *Store, emails Emails) *NotifyStore {
	return &NotifyStore{Store: s, emails: emails}
}

var _ notify.Store = (*NotifyStore)(nil)

// DueUsers truy vấn có index trên next_notify_at, không quét toàn bộ người dùng.
func (n *NotifyStore) DueUsers(ctx context.Context, now time.Time, limit int) ([]notify.DueUser, error) {
	docs, err := n.c.Collection("users").
		Where("settings.remind_enabled", "==", true).
		Where("next_notify_at", "<=", now).
		OrderBy("next_notify_at", firestore.Asc).
		Limit(limit).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("due users: %w", err)
	}
	out := make([]notify.DueUser, 0, len(docs))
	for _, d := range docs {
		u := notify.DueUser{UID: d.Ref.ID, Timezone: "Asia/Ho_Chi_Minh"}
		if st, ok := d.Data()["settings"].(map[string]any); ok {
			if tz, _ := st["timezone"].(string); tz != "" {
				u.Timezone = tz
			}
		}
		u.Name, _ = d.Data()["display_name"].(string)
		if email, err := n.emails.Email(ctx, u.UID); err == nil {
			u.Email = email
		}
		out = append(out, u)
	}
	return out, nil
}

// BuildDigest: chỉ tên món sắp hết, không có giá (thư có thể bị người khác đọc).
func (n *NotifyStore) BuildDigest(ctx context.Context, uid string, today domain.Date) (notify.Content, bool, error) {
	recs, err := n.ListItems(ctx, uid)
	if err != nil {
		return notify.Content{}, false, err
	}
	var lines []string
	for _, r := range recs {
		v := domain.ViewAt(r.Prediction, today, 7)
		if !r.Dropped && domain.InNeeds(v, 3) {
			lines = append(lines, r.Name)
		}
	}
	if len(lines) == 0 {
		return notify.Content{}, false, nil
	}
	return notify.Content{Subject: fmt.Sprintf("Mimi: %d món sắp hết", len(lines)), Lines: lines}, true, nil
}

func (n *NotifyStore) ClaimDigest(ctx context.Context, uid, id string) (bool, error) {
	_, err := n.user(uid).Collection("digests").Doc(id).Create(ctx, map[string]any{
		"status": "sending", "created_at": time.Now(),
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	return err == nil, err
}

func (n *NotifyStore) MarkDigest(ctx context.Context, uid, id, st string) error {
	_, err := n.user(uid).Collection("digests").Doc(id).Set(ctx, map[string]any{
		"status": st, "updated_at": time.Now(),
	}, firestore.MergeAll)
	return err
}

func (n *NotifyStore) Reschedule(ctx context.Context, uid string, next time.Time) error {
	_, err := n.user(uid).Update(ctx, []firestore.Update{{Path: "next_notify_at", Value: next}})
	return err
}

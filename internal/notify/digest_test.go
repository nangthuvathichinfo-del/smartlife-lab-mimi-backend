package notify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

type memStore struct {
	users    []DueUser
	claimed  map[string]bool
	marks    map[string]string
	resched  map[string]time.Time
	hasItems bool
}

func (m *memStore) DueUsers(context.Context, time.Time, int) ([]DueUser, error) { return m.users, nil }
func (m *memStore) BuildDigest(context.Context, string, domain.Date) (Content, bool, error) {
	return Content{Subject: "Mimi: 2 món sắp hết", Lines: []string{"Sữa tươi", "Trứng gà"}}, m.hasItems, nil
}
func (m *memStore) ClaimDigest(_ context.Context, uid, id string) (bool, error) {
	k := uid + "/" + id
	if m.claimed[k] {
		return false, nil
	}
	m.claimed[k] = true
	return true, nil
}
func (m *memStore) MarkDigest(_ context.Context, uid, id, st string) error {
	m.marks[uid+"/"+id] = st
	return nil
}
func (m *memStore) Reschedule(_ context.Context, uid string, t time.Time) error {
	m.resched[uid] = t
	return nil
}

type countMailer struct{ n int }

func (c *countMailer) Send(context.Context, string, string, []string) error { c.n++; return nil }

func TestDigest(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	newStore := func() *memStore {
		return &memStore{users: []DueUser{{UID: "u1", Timezone: "Asia/Ho_Chi_Minh", Email: "a@b.c"}},
			claimed: map[string]bool{}, marks: map[string]string{}, resched: map[string]time.Time{}, hasItems: true}
	}

	t.Run("second run same day does not resend", func(t *testing.T) {
		st, mail := newStore(), &countMailer{}
		noon := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC) // 12h giờ VN
		j := NewDigestJob(st, mail, log, func() time.Time { return noon })
		for i := 0; i < 2; i++ {
			if _, err := j.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if mail.n != 1 || st.marks["u1/2026-10-16_daily"] != "sent" {
			t.Fatalf("sent %d marks %v", mail.n, st.marks)
		}
	})

	t.Run("quiet hours reschedule to 7am", func(t *testing.T) {
		st, mail := newStore(), &countMailer{}
		late := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC) // 22h giờ VN
		j := NewDigestJob(st, mail, log, func() time.Time { return late })
		res, _ := j.Run(context.Background())
		want := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) // 7h sáng giờ VN
		if mail.n != 0 || res.Skipped != 1 || !st.resched["u1"].Equal(want) {
			t.Fatalf("sent %d res %+v next %v", mail.n, res, st.resched["u1"])
		}
	})
}

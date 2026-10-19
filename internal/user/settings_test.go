package user

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
)

type memSettings struct{ doc Stored }

func (m *memSettings) UpdateSettings(_ context.Context, _ string, apply func(Stored) (Stored, error)) (Stored, error) {
	next, err := apply(m.doc)
	if err != nil {
		return Stored{}, err
	}
	m.doc = next
	return next, nil
}

func ptr[T any](v T) *T { return &v }

func TestValidate(t *testing.T) {
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 41 ký tự
	bad := map[string]SettingsPatch{
		"empty":        {},
		"display_name": {DisplayName: &long},
		"lead_days":    {LeadDays: ptr(31)},
		"lead_days_0":  {LeadDays: ptr(0)},
		"digest_time":  {DigestTime: ptr("8h")},
		"timezone":     {Timezone: ptr("Mars/Base")},
		"channels":     {Channels: &[]string{"email", "email"}},
		"channels_bad": {Channels: &[]string{"sms"}},
		"lang":         {Lang: ptr("fr")},
		"remind_freq":  {RemindFreq: ptr("Every Day!")},
	}
	for name, p := range bad {
		var ae *apperr.Error
		if err := p.Validate(); !errors.As(err, &ae) || ae.Code != apperr.CodeValidation {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	ok := SettingsPatch{DisplayName: ptr("  Lan  "), LeadDays: ptr(5), DigestTime: ptr("07:30"), Timezone: ptr("Asia/Ho_Chi_Minh"), Channels: &[]string{"email", "in_app"}, Lang: ptr("en")}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMergesAndReschedules(t *testing.T) {
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC) // 10h sáng giờ VN
	cur := Stored{DisplayName: "Lan", Settings: map[string]any{"lead_days": int64(7), "remind_enabled": true, "timezone": "Asia/Ho_Chi_Minh", "lang": "vi"}}

	got := SettingsPatch{LeadDays: ptr(3), DigestTime: ptr("19:30")}.Apply(cur, now)
	if got.Settings["lead_days"] != int64(3) || got.Settings["lang"] != "vi" || got.DisplayName != "Lan" {
		t.Fatalf("merge: %+v", got)
	}
	want := time.Date(2026, 10, 18, 12, 30, 0, 0, time.UTC) // 19:30 giờ VN hôm nay
	if got.NextNotifyAt == nil || !got.NextNotifyAt.Equal(want) {
		t.Fatalf("next notify %v", got.NextNotifyAt)
	}
	if cur.Settings["lead_days"] != int64(7) {
		t.Fatal("Apply must not mutate the current settings")
	}

	// Không đổi lịch nhắc thì không ghi next_notify_at.
	if g := (SettingsPatch{Lang: ptr("en")}).Apply(cur, now); g.NextNotifyAt != nil {
		t.Fatalf("unexpected reschedule %v", g.NextNotifyAt)
	}
	// Tắt nhắc thì không lên lịch.
	if g := (SettingsPatch{RemindEnabled: ptr(false)}).Apply(cur, now); g.NextNotifyAt != nil || g.Settings["remind_enabled"] != false {
		t.Fatalf("remind off: %+v", g)
	}
	// Bật nhắc lần đầu, chưa có giờ: dùng 08:00 ngày mai (10h sáng nay đã qua 8h).
	off := Stored{Settings: map[string]any{"remind_enabled": false}}
	g := SettingsPatch{RemindEnabled: ptr(true)}.Apply(off, now)
	if g.NextNotifyAt == nil || !g.NextNotifyAt.Equal(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("remind on: %v", g.NextNotifyAt)
	}
}

func TestSettingsServiceUpdate(t *testing.T) {
	st := &memSettings{doc: Stored{DisplayName: "Lan", Settings: nil}}
	svc := NewSettingsService(st, func() time.Time { return time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC) })
	p := authz.Principal{UID: "u1", Role: authz.RoleMember}

	view, err := svc.Update(context.Background(), p, SettingsPatch{DisplayName: ptr(" Minh "), Channels: &[]string{"email"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(view)
	if string(b) != `{"channels":["email"],"display_name":"Minh"}` {
		t.Fatalf("view %s", b)
	}
	if _, err := svc.Update(context.Background(), authz.Principal{}, SettingsPatch{Lang: ptr("en")}); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
}

package user

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

// SettingsPatch là thân PUT /settings. Trường vắng mặt giữ nguyên giá trị cũ, nên
// client chỉ cần gửi phần thay đổi. Trường lạ bị từ chối ở lớp HTTP.
type SettingsPatch struct {
	DisplayName        *string   `json:"display_name"`
	LeadDays           *int      `json:"lead_days"`
	RemindEnabled      *bool     `json:"remind_enabled"`
	RemindFreq         *string   `json:"remind_freq"`
	DigestTime         *string   `json:"digest_time"`
	Timezone           *string   `json:"timezone"`
	Channels           *[]string `json:"channels"`
	ReplacementEnabled *bool     `json:"replacement_enabled"`
	SaleEnabled        *bool     `json:"sale_enabled"`
	Lang               *string   `json:"lang"`
}

// Stored là phần tài liệu users/{uid} mà cài đặt được phép đọc và ghi.
type Stored struct {
	DisplayName  string
	Settings     map[string]any
	NextNotifyAt *time.Time // khác nil: ghi next_notify_at để job digest lên lịch lại
}

// SettingsStore cập nhật trong transaction: đọc bản hiện tại, gọi apply (thuần, không
// I/O), rồi ghi display_name, settings và next_notify_at (nếu có).
type SettingsStore interface {
	UpdateSettings(ctx context.Context, uid string, apply func(cur Stored) (Stored, error)) (Stored, error)
}

var remindFreqRe = regexp.MustCompile(`^[a-z_]{1,20}$`)

const maxDisplayName = 40

// Validate trả lỗi 422 kèm tên trường đầu tiên không hợp lệ.
func (p SettingsPatch) Validate() error {
	bad := func(field, msg string) error {
		return apperr.New(apperr.CodeValidation, msg).With("field", field)
	}
	if p == (SettingsPatch{}) {
		return apperr.New(apperr.CodeValidation, "Chưa có thay đổi nào để lưu.")
	}
	if p.DisplayName != nil {
		n := strings.TrimSpace(*p.DisplayName)
		if utf8.RuneCountInString(n) > maxDisplayName || strings.IndexFunc(n, unicode.IsControl) >= 0 {
			return bad("display_name", "Tên hiển thị tối đa 40 ký tự.")
		}
	}
	if p.LeadDays != nil && (*p.LeadDays < 1 || *p.LeadDays > 30) {
		return bad("lead_days", "Số ngày báo trước từ 1 đến 30.")
	}
	if p.RemindFreq != nil && !remindFreqRe.MatchString(*p.RemindFreq) {
		return bad("remind_freq", "Tần suất nhắc không hợp lệ.")
	}
	if p.DigestTime != nil {
		if _, _, ok := domain.ParseClock(*p.DigestTime); !ok {
			return bad("digest_time", "Giờ nhận tin có dạng HH:MM.")
		}
	}
	if p.Timezone != nil {
		if *p.Timezone == "" || len(*p.Timezone) > 64 {
			return bad("timezone", "Múi giờ không hợp lệ.")
		}
		if _, err := time.LoadLocation(*p.Timezone); err != nil {
			return bad("timezone", "Múi giờ không hợp lệ.")
		}
	}
	if p.Channels != nil {
		seen := map[string]bool{}
		for _, c := range *p.Channels {
			if (c != "in_app" && c != "email") || seen[c] {
				return bad("channels", "Kênh nhận tin chỉ gồm in_app và email, không lặp.")
			}
			seen[c] = true
		}
	}
	if p.Lang != nil && *p.Lang != "vi" && *p.Lang != "en" {
		return bad("lang", "Ngôn ngữ dịch hóa đơn chỉ là vi hoặc en.")
	}
	return nil
}

// Apply gộp bản vá vào cài đặt hiện tại và tính lại lịch gửi bản tin khi cần.
// Hàm thuần: nhận now từ ngoài để test được.
func (p SettingsPatch) Apply(cur Stored, now time.Time) Stored {
	next := Stored{DisplayName: cur.DisplayName, Settings: make(map[string]any, len(cur.Settings)+8)}
	for k, v := range cur.Settings {
		next.Settings[k] = v
	}
	if p.DisplayName != nil {
		next.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	set := func(k string, v any) { next.Settings[k] = v }
	if p.LeadDays != nil {
		set("lead_days", int64(*p.LeadDays)) // Firestore trả số nguyên dạng int64; giữ cùng kiểu
	}
	if p.RemindEnabled != nil {
		set("remind_enabled", *p.RemindEnabled)
	}
	if p.RemindFreq != nil {
		set("remind_freq", *p.RemindFreq)
	}
	if p.DigestTime != nil {
		set("digest_time", *p.DigestTime)
	}
	if p.Timezone != nil {
		set("timezone", *p.Timezone)
	}
	if p.Channels != nil {
		chs := make([]any, 0, len(*p.Channels))
		for _, c := range *p.Channels {
			chs = append(chs, c)
		}
		set("channels", chs)
	}
	if p.ReplacementEnabled != nil {
		set("replacement_enabled", *p.ReplacementEnabled)
	}
	if p.SaleEnabled != nil {
		set("sale_enabled", *p.SaleEnabled)
	}
	if p.Lang != nil {
		set("lang", *p.Lang)
	}

	// Đổi lịch nhắc thì tính lại lượt gửi kế tiếp; job digest chỉ đọc next_notify_at.
	scheduleChanged := p.RemindEnabled != nil || p.DigestTime != nil || p.Timezone != nil
	if on, _ := next.Settings["remind_enabled"].(bool); on && scheduleChanged {
		h, m := domain.DefaultDigestHour, domain.DefaultDigestMinute
		if s, _ := next.Settings["digest_time"].(string); s != "" {
			if hh, mm, ok := domain.ParseClock(s); ok {
				h, m = hh, mm
			}
		}
		tz, _ := next.Settings["timezone"].(string)
		at := domain.NextDigestAt(now, domain.Location(tz), h, m)
		next.NextNotifyAt = &at
	}
	return next
}

// View là phản hồi PUT /settings: cài đặt sau khi lưu, kèm tên hiển thị.
func (s Stored) View() map[string]any {
	out := make(map[string]any, len(s.Settings)+1)
	for k, v := range s.Settings {
		out[k] = v
	}
	out["display_name"] = s.DisplayName
	return out
}

type SettingsService struct {
	store SettingsStore
	now   func() time.Time
}

func NewSettingsService(store SettingsStore, now func() time.Time) *SettingsService {
	if now == nil {
		now = time.Now
	}
	return &SettingsService{store: store, now: now}
}

func (s *SettingsService) Update(ctx context.Context, p authz.Principal, patch SettingsPatch) (map[string]any, error) {
	if err := authz.Can(p, authz.ActionWriteSettings); err != nil {
		return nil, err
	}
	if err := patch.Validate(); err != nil {
		return nil, err
	}
	now := s.now()
	saved, err := s.store.UpdateSettings(ctx, p.UID, func(cur Stored) (Stored, error) {
		return patch.Apply(cur, now), nil
	})
	if err != nil {
		return nil, err
	}
	return saved.View(), nil
}

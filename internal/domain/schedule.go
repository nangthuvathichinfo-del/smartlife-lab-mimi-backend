package domain

import (
	"strconv"
	"time"
)

// DefaultDigestHour, DefaultDigestMinute: giờ gửi bản tin mặc định khi người dùng chưa chọn.
const (
	DefaultDigestHour   = 8
	DefaultDigestMinute = 0
)

// Location trả múi giờ theo tên IANA; tên rỗng hoặc không hợp lệ thì dùng giờ Việt Nam.
func Location(name string) *time.Location {
	if name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	return time.FixedZone("ICT", 7*3600)
}

// ParseClock đọc "HH:MM" (00:00 đến 23:59). ok=false nếu sai định dạng.
func ParseClock(s string) (hour, minute int, ok bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// NextDigestAt trả lần gửi bản tin kế tiếp: hour:minute theo giờ người dùng, sau now,
// rồi dời ra khỏi giờ yên lặng nếu cần.
func NextDigestAt(now time.Time, loc *time.Location, hour, minute int) time.Time {
	l := now.In(loc)
	next := time.Date(l.Year(), l.Month(), l.Day(), hour, minute, 0, 0, loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return NextAllowed(next, loc)
}

// MonthOf trả "YYYY-MM" của thời điểm now theo múi giờ loc.
func MonthOf(now time.Time, loc *time.Location) string {
	return now.In(loc).Format("2006-01")
}

// ParseMonth kiểm tra "YYYY-MM" và trả về tháng liền trước. ok=false nếu sai định dạng.
func ParseMonth(s string) (prev string, ok bool) {
	t, err := time.Parse("2006-01", s)
	if err != nil || t.Format("2006-01") != s || t.Year() < 2000 || t.Year() > 2100 {
		return "", false
	}
	return t.AddDate(0, -1, 0).Format("2006-01"), true
}

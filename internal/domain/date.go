package domain

import (
	"fmt"
	"time"
)

// Date là một ngày lịch (không giờ, không múi giờ), ví dụ ngày mua in trên hóa đơn.
// Lưu và truyền dưới dạng "YYYY-MM-DD". "Hôm nay" luôn được tính theo múi giờ của
// người dùng rồi mới chuyển thành Date, để 23h30 tối ở Việt Nam không bị tính sang ngày mới theo UTC.
type Date struct{ t time.Time }

const dateLayout = "2006-01-02"

func NewDate(y int, m time.Month, d int) Date {
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("domain: invalid date %q: %w", s, err)
	}
	return Date{t}, nil
}

// Today trả về ngày hiện tại theo múi giờ loc.
func Today(now time.Time, loc *time.Location) Date {
	l := now.In(loc)
	return NewDate(l.Year(), l.Month(), l.Day())
}

func (d Date) String() string     { return d.t.Format(dateLayout) }
func (d Date) IsZero() bool       { return d.t.IsZero() }
func (d Date) AddDays(n int) Date { return Date{d.t.AddDate(0, 0, n)} }
func (d Date) Before(o Date) bool { return d.t.Before(o.t) }
func (d Date) After(o Date) bool  { return d.t.After(o.t) }
func (d Date) Equal(o Date) bool  { return d.t.Equal(o.t) }
func (d Date) Month() string      { return d.t.Format("2006-01") }

// At trả về thời điểm hour:min của ngày d theo múi giờ loc.
func (d Date) At(hour, min int, loc *time.Location) time.Time {
	return time.Date(d.t.Year(), d.t.Month(), d.t.Day(), hour, min, 0, 0, loc)
}

// DaysUntil trả về số ngày từ d tới o (âm nếu o ở trước d).
func (d Date) DaysUntil(o Date) int {
	return int(o.t.Sub(d.t).Hours() / 24)
}

func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Date) UnmarshalText(b []byte) error {
	p, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}

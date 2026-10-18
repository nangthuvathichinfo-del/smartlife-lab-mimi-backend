package domain

import "time"

// Giờ yên lặng 21h đến 7h theo múi giờ người dùng (spec mục 6, Thông báo).
const (
	QuietStartHour = 21
	QuietEndHour   = 7
)

// InQuietHours cho biết thời điểm now có rơi vào giờ yên lặng của người dùng không.
func InQuietHours(now time.Time, loc *time.Location) bool {
	h := now.In(loc).Hour()
	return h >= QuietStartHour || h < QuietEndHour
}

// NextAllowed trả về thời điểm sớm nhất được gửi thông báo (now nếu ngoài giờ yên lặng).
func NextAllowed(now time.Time, loc *time.Location) time.Time {
	if !InQuietHours(now, loc) {
		return now
	}
	l := now.In(loc)
	next := time.Date(l.Year(), l.Month(), l.Day(), QuietEndHour, 0, 0, 0, loc)
	if l.Hour() >= QuietStartHour {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

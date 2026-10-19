package domain

import (
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	for _, tc := range []struct {
		in   string
		h, m int
		ok   bool
	}{
		{"08:00", 8, 0, true}, {"23:59", 23, 59, true}, {"00:00", 0, 0, true},
		{"24:00", 0, 0, false}, {"8:00", 0, 0, false}, {"08:60", 0, 0, false}, {"ab:cd", 0, 0, false}, {"", 0, 0, false},
	} {
		h, m, ok := ParseClock(tc.in)
		if ok != tc.ok || (ok && (h != tc.h || m != tc.m)) {
			t.Errorf("ParseClock(%q) = %d,%d,%v", tc.in, h, m, ok)
		}
	}
}

func TestNextDigestAt(t *testing.T) {
	vn := Location("Asia/Ho_Chi_Minh")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, vn)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		now, want string
		h, m      int
	}{
		{"2026-10-18 06:00", "2026-10-18 08:00", 8, 0},  // hôm nay, chưa tới giờ
		{"2026-10-18 09:00", "2026-10-19 08:00", 8, 0},  // đã qua giờ: ngày mai
		{"2026-10-18 08:00", "2026-10-19 08:00", 8, 0},  // đúng giờ: lượt sau
		{"2026-10-18 10:00", "2026-10-19 07:00", 22, 0}, // 22h là giờ yên lặng: dời tới 7h sáng
	}
	for _, c := range cases {
		if got := NextDigestAt(at(c.now), vn, c.h, c.m); !got.Equal(at(c.want)) {
			t.Errorf("now %s at %02d:%02d: got %s want %s", c.now, c.h, c.m, got.In(vn), c.want)
		}
	}
}

func TestParseMonth(t *testing.T) {
	if p, ok := ParseMonth("2026-01"); !ok || p != "2025-12" {
		t.Fatalf("got %q %v", p, ok)
	}
	for _, bad := range []string{"2026-13", "2026-1", "26-01", "2026-01-01", "", "1999-12"} {
		if _, ok := ParseMonth(bad); ok {
			t.Errorf("ParseMonth(%q) should fail", bad)
		}
	}
}

func TestLocationFallback(t *testing.T) {
	if _, off := time.Date(2026, 1, 1, 0, 0, 0, 0, Location("Not/AZone")).Zone(); off != 7*3600 {
		t.Fatalf("fallback offset %d", off)
	}
}

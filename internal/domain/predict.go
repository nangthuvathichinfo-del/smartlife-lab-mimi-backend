package domain

import (
	"math"
	"sort"
	"time"
)

// Purchase là một lần mua của một nhóm tiêu dùng, lấy từ purchases.
// Habit=false (món của chuyến đi) không được dùng để dự đoán.
type Purchase struct {
	On    Date
	Habit bool
}

// Override là bản sửa tay "Còn nhiều / Sắp hết / Đã hết / N ngày" của người dùng.
// SetOn là ngày người dùng bấm (theo múi giờ của họ), SetAt là thời điểm chính xác để
// so với các lần sửa khác. Bản sửa chỉ có hiệu lực khi mới hơn lần mua gần nhất.
type Override struct {
	Days  int // 0 nghĩa là "Đã hết"
	Out   bool
	SetOn Date
	SetAt time.Time
}

// Quick stock theo spec mục 4.
const (
	QuickPlentyDays = 30
	QuickSoonDays   = 3
	QuickOutDays    = 0
	MinOverrideDays = 1
	MaxOverrideDays = 60
)

type Confidence string

const (
	ConfidenceNone Confidence = ""
	ConfidenceLow  Confidence = "low"
	ConfidenceMid  Confidence = "mid"
	ConfidenceHigh Confidence = "high"
)

type Source string

const (
	SourcePredicted Source = "predicted"
	SourceManual    Source = "manual"
)

// Prediction là kết quả cho một nhóm tiêu dùng. OutOn là ngày dự kiến hết;
// "số ngày còn lại" được tính lúc đọc từ OutOn và hôm nay, không lưu.
type Prediction struct {
	OutOn         Date // zero nếu chưa đủ dữ liệu
	IntervalDays  int  // khoảng mua lặp lại (trung vị), 0 nếu chưa có
	PurchaseCount int
	Confidence    Confidence
	Source        Source
	ForcedOut     bool // người dùng bấm "Đã hết"
	Preliminary   bool // 2 lần mua: chỉ hiện khoảng cách và ngày dự kiến
}

// maxIntervals: trung vị tối đa 3 khoảng mua gần nhất (spec mục 4, quy tắc).
const maxIntervals = 3

// Predict tính dự đoán cho một nhóm từ lịch sử mua và bản sửa tay.
// Không đoán hạn dùng; nhóm dịch vụ không được gọi hàm này.
func Predict(purchases []Purchase, ov *Override) Prediction {
	dates := habitDates(purchases)
	p := Prediction{PurchaseCount: len(dates), Source: SourcePredicted}

	if len(dates) >= 2 {
		intervals := make([]int, 0, len(dates)-1)
		for i := 1; i < len(dates); i++ {
			intervals = append(intervals, dates[i-1].DaysUntil(dates[i]))
		}
		if len(intervals) > maxIntervals {
			intervals = intervals[len(intervals)-maxIntervals:]
		}
		p.IntervalDays = median(intervals)
		p.OutOn = dates[len(dates)-1].AddDays(p.IntervalDays)
		p.Confidence = confidenceFor(len(dates), intervals, p.IntervalDays)
		p.Preliminary = len(dates) == 2
	}

	if ov != nil && overrideActive(ov, dates) {
		p.Source = SourceManual
		p.ForcedOut = ov.Out
		p.OutOn = ov.SetOn.AddDays(ov.Days)
		p.Preliminary = false
	}
	return p
}

// overrideActive: bản sửa tay mới hơn lần mua gần nhất thì thắng. Có hóa đơn mới hơn
// cùng nhóm thì bản sửa tự hết hiệu lực, không cần xóa (kết quả không phụ thuộc thứ tự
// request tới server, kể cả khi hàng đợi offline gửi muộn).
func overrideActive(ov *Override, dates []Date) bool {
	if len(dates) == 0 {
		return true
	}
	last := dates[len(dates)-1]
	return !ov.SetOn.Before(last)
}

func habitDates(ps []Purchase) []Date {
	seen := make(map[string]bool, len(ps))
	out := make([]Date, 0, len(ps))
	for _, p := range ps {
		if !p.Habit || p.On.IsZero() {
			continue
		}
		k := p.On.String()
		if seen[k] { // nhiều hóa đơn cùng ngày tính là một lần mua
			continue
		}
		seen[k] = true
		out = append(out, p.On)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func median(xs []int) int {
	s := append([]int(nil), xs...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return int(math.Round(float64(s[n/2-1]+s[n/2]) / 2))
}

// confidenceFor: thấp với 2 lần mua, vừa với 3, cao từ 4 lần và các khoảng lệch
// không quá 25% so với trung vị (spec mục 4, Chi tiết món).
func confidenceFor(purchases int, intervals []int, med int) Confidence {
	switch {
	case purchases < 2:
		return ConfidenceNone
	case purchases == 2:
		return ConfidenceLow
	case purchases == 3:
		return ConfidenceMid
	}
	if med <= 0 {
		return ConfidenceMid
	}
	for _, iv := range intervals {
		if math.Abs(float64(iv-med))/float64(med) > 0.25 {
			return ConfidenceMid
		}
	}
	return ConfidenceHigh
}

// OverrideFromQuick đổi lựa chọn nhanh thành bản sửa tay.
func OverrideFromQuick(kind string, setOn Date, setAt time.Time) (Override, bool) {
	switch kind {
	case "plenty":
		return Override{Days: QuickPlentyDays, SetOn: setOn, SetAt: setAt}, true
	case "soon":
		return Override{Days: QuickSoonDays, SetOn: setOn, SetAt: setAt}, true
	case "out":
		return Override{Days: QuickOutDays, Out: true, SetOn: setOn, SetAt: setAt}, true
	}
	return Override{}, false
}

// OverrideFromDays: người dùng nhập số ngày từ 1 đến 60.
func OverrideFromDays(days int, setOn Date, setAt time.Time) (Override, bool) {
	if days < MinOverrideDays || days > MaxOverrideDays {
		return Override{}, false
	}
	return Override{Days: days, SetOn: setOn, SetAt: setAt}, true
}

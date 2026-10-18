package domain

// Status là nhãn trạng thái dùng chung mọi màn (spec 6.2), theo thứ tự ưu tiên.
type Status string

const (
	StatusOut      Status = "out"      // Đã hết (người dùng bấm)
	StatusBuyNow   Status = "buy_now"  // Đến lúc mua: còn 0 ngày hoặc đã quá ngày dự kiến
	StatusSoon     Status = "soon"     // Sắp hết: 1 đến lead_days ngày
	StatusUpcoming Status = "upcoming" // Sắp tới: đến 14 ngày
	StatusPlenty   Status = "plenty"   // Còn nhiều
	StatusWaiting  Status = "waiting"  // Chưa đủ dữ liệu
)

const UpcomingHorizonDays = 14

// View là trạng thái của một món tại một ngày cụ thể.
type View struct {
	Status   Status
	DaysLeft *int // nil khi chưa đủ dữ liệu
}

// ViewAt tính trạng thái hiển thị từ dự đoán và hôm nay. leadDays lấy từ cài đặt
// (1 đến 30, mặc định 7). Spec còn ba ngưỡng "sắp hết" chưa gộp (mục 12.2); hàm này
// dùng một ngưỡng duy nhất là leadDays cho tới khi chủ sản phẩm chốt.
func ViewAt(p Prediction, today Date, leadDays int) View {
	if p.ForcedOut {
		zero := 0
		return View{Status: StatusOut, DaysLeft: &zero}
	}
	// Từ 3 lần mua mới có số ngày còn lại; bản sửa tay luôn có.
	if p.OutOn.IsZero() || (p.Source == SourcePredicted && p.PurchaseCount < 3) {
		return View{Status: StatusWaiting}
	}
	days := today.DaysUntil(p.OutOn)
	if days < 0 {
		days = 0
	}
	v := View{DaysLeft: &days}
	switch {
	case days == 0:
		v.Status = StatusBuyNow
	case days <= leadDays:
		v.Status = StatusSoon
	case days <= UpcomingHorizonDays:
		v.Status = StatusUpcoming
	default:
		v.Status = StatusPlenty
	}
	return v
}

// InNeeds: món có nằm trong "Cần mua" với khoảng ngày horizon (3, 7, 14) không.
func InNeeds(v View, horizon int) bool {
	if v.Status == StatusOut || v.Status == StatusBuyNow {
		return true
	}
	return v.DaysLeft != nil && v.Status != StatusWaiting && *v.DaysLeft <= horizon
}

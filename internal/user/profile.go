// Package user chứa kiểu dữ liệu hồ sơ dùng chung giữa lớp HTTP và lớp lưu trữ.
package user

// Profile là phản hồi GET /me. Email nằm ở Firebase Auth, không lưu ở users/{uid}.
type Profile struct {
	UID          string         `json:"uid"`
	DisplayName  string         `json:"display_name"`
	Role         string         `json:"role"`
	HouseholdID  string         `json:"household_id,omitempty"`
	ReceiptCount int            `json:"receipt_count"`
	Settings     map[string]any `json:"settings"`
}

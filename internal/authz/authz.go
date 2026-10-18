// Package authz là nơi duy nhất quyết định "ai được làm gì" (spec mục 2, 3, 6).
// Phạm vi dữ liệu được bảo đảm bằng cấu trúc: mọi đường dẫn Firestore đều ghép từ
// Principal.UID, không bao giờ lấy uid từ request. Package này chỉ trả lời câu hỏi
// về vai trò.
package authz

import "github.com/smartlife-lab/mimi/backend/internal/apperr"

// Role bắt đầu bằng RoleUnknown để giá trị rỗng không vô tình mang quyền.
type Role int

const (
	RoleUnknown Role = iota
	RoleOwner        // chủ giỏ, hoặc người dùng một mình (nhà một người)
	RoleMember       // thành viên nhà: chỉ thấy món sắp hết và lời nhờ mua, không thấy giá
)

func (r Role) String() string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleMember:
		return "member"
	}
	return "unknown"
}

// Principal là người đang gọi API, dựng từ ID token và users/{uid}.
type Principal struct {
	UID         string
	HouseholdID string // rỗng nếu chưa vào nhà nào
	Role        Role
	Timezone    string
	LeadDays    int
}

type Action int

const (
	ActionUnknown Action = iota
	ActionReadSelf
	ActionWriteSettings
	ActionDeleteSelf
	ActionReadReceipts
	ActionWriteReceipts
	ActionReadSpending
	ActionReadItemsFull
	ActionReadItemsShared // dạng rút gọn, không giá
	ActionWriteItems
	ActionReadPlan
	ActionWritePlan
	ActionManageRecurring
	ActionSendAsk
	ActionAnswerAsk
	ActionManageHousehold
	ActionJoinHousehold
	ActionLeaveHousehold
)

var ownerOnly = map[Action]bool{
	ActionReadReceipts: true, ActionWriteReceipts: true, ActionReadSpending: true,
	ActionReadItemsFull: true, ActionWriteItems: true, ActionReadPlan: true,
	ActionWritePlan: true, ActionManageRecurring: true, ActionSendAsk: true,
	ActionManageHousehold: true,
}

var anyone = map[Action]bool{
	ActionReadSelf: true, ActionWriteSettings: true, ActionDeleteSelf: true,
	ActionReadItemsShared: true, ActionJoinHousehold: true,
}

var memberOnly = map[Action]bool{
	ActionAnswerAsk: true, ActionLeaveHousehold: true,
}

// Can trả nil nếu được phép, ngược lại trả lỗi 403/401 dùng được ngay.
func Can(p Principal, a Action) error {
	if p.UID == "" {
		return apperr.ErrUnauthenticated
	}
	switch {
	case anyone[a]:
		return nil
	case ownerOnly[a] && p.Role == RoleOwner:
		return nil
	case memberOnly[a] && p.Role == RoleMember:
		return nil
	}
	return apperr.ErrForbidden
}

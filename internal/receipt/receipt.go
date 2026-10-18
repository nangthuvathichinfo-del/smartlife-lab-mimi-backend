// Package receipt là luồng lõi của Mimi: đọc hóa đơn (Gemini) và lưu hóa đơn
// (transaction Firestore). "Chỉ hóa đơn mới đổi dữ liệu": đây là đường ghi duy nhất
// làm thay đổi tồn kho và chi tiêu.
//
// Các interface ở file này được định nghĩa ở phía dùng (package receipt), adapter
// Firestore và Gemini trong internal/gcp hiện thực chúng.
package receipt

import (
	"context"
	"errors"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

// Image là một ảnh hóa đơn đã nén ở máy người dùng. Chỉ nằm trong bộ nhớ request.
type Image struct {
	Data []byte
	MIME string // image/jpeg | image/png | image/webp
}

// PageLine là một dòng do AI đọc. Amount giữ nguyên dạng thập phân in trên hóa đơn
// ("64000", "12.50"); AI không tính tiền, code quy đổi.
type PageLine struct {
	Name        string
	Qty         float64
	Amount      string
	Discount    string // "" nếu không có
	CategoryKey string // khóa trong taxonomy 27 danh mục, "" nếu chưa chọn được
	GroupKey    string // nhóm tiêu dùng, "" nếu chưa có
}

// Page là kết quả đọc một ảnh.
type Page struct {
	Store         string
	PurchasedOn   string // YYYY-MM-DD hoặc ""
	Currency      string // ISO 4217, mặc định VND
	TotalPrinted  string // tổng in trên hóa đơn, "" nếu không có
	DiscountTotal string
	Lines         []PageLine
}

// ErrUnreadable: ảnh không phải hóa đơn, quá mờ, hoặc AI trả sai schema. Không retry.
var ErrUnreadable = errors.New("receipt: page unreadable")

// ErrTransient: AI quá tải hoặc lỗi tạm thời. Có thể retry trong ngân sách thời gian.
var ErrTransient = errors.New("receipt: transient AI failure")

// Reader đọc một trang. Adapter Gemini phải dùng structured output và trả ErrUnreadable
// hoặc ErrTransient (bọc bằng %w) cho hai loại lỗi trên.
type Reader interface {
	ReadPage(ctx context.Context, img Image, lang string) (Page, error)
}

// DraftLine là một dòng trong bản nháp gửi về màn Xác nhận.
type DraftLine struct {
	LineID      string      `json:"line_id"`
	Name        string      `json:"name"`
	Qty         float64     `json:"qty"`
	Amount      string      `json:"amount"`
	AmountVND   *domain.VND `json:"amount_vnd"` // nil khi chưa quy đổi được
	CategoryKey string      `json:"category_key,omitempty"`
	GroupKey    string      `json:"group_key,omitempty"`
	Habit       bool        `json:"habit"`
}

type FX struct {
	Rate   string    `json:"rate"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// Draft không chứa ảnh. Được lưu tạm (TTL 1 giờ) theo receipt_id để gửi lại không gọi Gemini lần nữa.
type Draft struct {
	ReceiptID       string      `json:"receipt_id"`
	Store           string      `json:"store,omitempty"`
	PurchasedOn     string      `json:"purchased_on,omitempty"`
	Currency        string      `json:"currency"`
	TotalPrinted    string      `json:"total_printed,omitempty"`
	DiscountTotal   string      `json:"discount_total,omitempty"`
	Lines           []DraftLine `json:"lines"`
	SumVND          domain.VND  `json:"sum_vnd"`
	TotalMismatch   bool        `json:"total_mismatch"`
	FX              *FX         `json:"fx,omitempty"`
	FXMissing       bool        `json:"fx_missing"`
	Pages           int         `json:"pages"`
	UnreadablePages int         `json:"unreadable_pages"` // > 0: hiện "N ảnh chưa đọc được, chụp lại?"
}

type DraftCache interface {
	Get(ctx context.Context, uid, receiptID string) (*Draft, error) // (nil, nil) nếu không có
	Put(ctx context.Context, uid string, d Draft) error
}

// Quota: tối đa N lần đọc mỗi ngày (theo giờ người dùng). Reserve trước khi gọi AI,
// Release nếu đọc thất bại để lỗi của hệ thống không trừ lượt của người dùng.
type Quota interface {
	Reserve(ctx context.Context, uid, day string, limit int) error
	Release(ctx context.Context, uid, day string) error
}

// FXRates trả tỷ giá có nguồn cho một ngày; ok=false nếu chưa có. Không bao giờ tự đoán.
type FXRates interface {
	Rate(ctx context.Context, currency string, on domain.Date) (fx FX, ok bool, err error)
}

// Line là dòng hóa đơn đã lưu.
type Line struct {
	ID          string
	Name        string
	NameNorm    string
	Qty         float64
	Orig        string // số gốc in trên hóa đơn
	VND         *domain.VND
	CategoryKey string
	GroupKey    string
	Habit       bool
}

// Receipt là hóa đơn đã xác nhận, sẵn sàng ghi trong một transaction.
type Receipt struct {
	ID          string
	Store       string
	PurchasedOn domain.Date
	Currency    string
	FX          *FX
	Lines       []Line
	TotalVND    domain.VND // chỉ cộng các dòng đã quy đổi
	DiscountVND domain.VND
	DedupeKey   string // cửa hàng + ngày + tổng, để cảnh báo trùng
	PayloadHash string // để phân biệt "gửi lại" với "receipt_id trùng nhưng nội dung khác"
	SavedAt     time.Time
}

type SaveOutcome struct {
	ReceiptID string
	Replayed  bool // cùng receipt_id, cùng nội dung: trả kết quả cũ
}

// Store ghi hóa đơn và mọi dữ liệu suy ra trong MỘT transaction: receipt, purchases,
// items của các nhóm bị ảnh hưởng (tính lại bằng domain.Predict), months, plan_items
// chuyển done, asks khớp nhóm đóng lại, receipt_count tăng.
type Store interface {
	SaveReceipt(ctx context.Context, uid string, r Receipt, tz *time.Location) (SaveOutcome, error)
	HasSimilar(ctx context.Context, uid, dedupeKey, exceptID string) (bool, error)
}

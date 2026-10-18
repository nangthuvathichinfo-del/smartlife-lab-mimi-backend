// Package domain chứa logic nghiệp vụ thuần của Mimi: tiền, ngày, dự đoán tồn kho,
// nhãn trạng thái, nơi mua tối ưu. Package này không gọi mạng, không đọc Firestore,
// không phụ thuộc thời gian thực (mọi "hôm nay" đều được truyền vào), nên test được
// bằng dữ liệu mẫu của bản demo.
package domain

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// VND là số tiền tính bằng đồng, luôn là số nguyên. Không bao giờ dùng float cho tiền.
type VND int64

// MaxLineVND chặn giá trị phi lý (lỗi đọc hoặc dữ liệu giả mạo): 1 tỷ đồng mỗi dòng.
const MaxLineVND VND = 1_000_000_000

var (
	ErrNegativeAmount = errors.New("domain: amount must not be negative")
	ErrAmountTooLarge = errors.New("domain: amount exceeds limit")
	ErrBadDecimal     = errors.New("domain: invalid decimal string")
)

// NewVND kiểm tra một số tiền dòng hóa đơn.
func NewVND(v int64) (VND, error) {
	switch {
	case v < 0:
		return 0, ErrNegativeAmount
	case VND(v) > MaxLineVND:
		return 0, ErrAmountTooLarge
	}
	return VND(v), nil
}

// Sum cộng các khoản; trả lỗi nếu tràn giới hạn hợp lý.
func Sum(xs ...VND) (VND, error) {
	var total VND
	for _, x := range xs {
		if x < 0 {
			return 0, ErrNegativeAmount
		}
		total += x
		if total > MaxLineVND*1000 {
			return 0, ErrAmountTooLarge
		}
	}
	return total, nil
}

// ConvertToVND quy đổi một số tiền ngoại tệ dạng thập phân ("12.50") theo tỷ giá dạng
// thập phân ("18450.25") sang đồng. Tính bằng số hữu tỉ chính xác, làm tròn một lần ở
// cuối theo kiểu "nửa xa số 0". Tỷ giá phải đến từ fx_rates có nguồn; hàm này không
// bao giờ tự chọn tỷ giá.
func ConvertToVND(amount, rate string) (VND, error) {
	a, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	if !ok {
		return 0, fmt.Errorf("%w: amount %q", ErrBadDecimal, amount)
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(rate))
	if !ok || r.Sign() <= 0 {
		return 0, fmt.Errorf("%w: rate %q", ErrBadDecimal, rate)
	}
	if a.Sign() < 0 {
		return 0, ErrNegativeAmount
	}
	product := new(big.Rat).Mul(a, r)
	// Làm tròn nửa xa số 0: floor(x + 1/2) với x >= 0.
	half := big.NewRat(1, 2)
	product.Add(product, half)
	q := new(big.Int).Quo(product.Num(), product.Denom())
	if !q.IsInt64() {
		return 0, ErrAmountTooLarge
	}
	return NewVND(q.Int64())
}

package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// viFold ánh xạ mọi chữ có dấu của tiếng Việt về chữ không dấu. Dùng bảng tĩnh thay vì
// golang.org/x/text: đủ cho tiếng Việt, không thêm phụ thuộc. "đ" phải map riêng vì
// chuẩn hóa NFD cũng không tách được chữ này.
var viFold = func() map[rune]rune {
	groups := map[rune]string{
		'a': "àáảãạăằắẳẵặâầấẩẫậ",
		'e': "èéẻẽẹêềếểễệ",
		'i': "ìíỉĩị",
		'o': "òóỏõọôồốổỗộơờớởỡợ",
		'u': "ùúủũụưừứửữự",
		'y': "ỳýỷỹỵ",
		'd': "đ",
	}
	m := make(map[rune]rune, 160)
	for base, accented := range groups {
		for _, r := range accented {
			m[r] = base
			m[unicode.ToUpper(r)] = base
		}
	}
	return m
}()

// NormalizeVi chuẩn hóa để tìm không dấu: "Dầu gội" và "dau goi" khớp nhau.
// Đầu vào giả định đã ở dạng dựng sẵn (NFC), là dạng bàn phím điện thoại gửi lên;
// dấu kết hợp rời (combining marks) vẫn được bỏ qua để an toàn.
func NormalizeVi(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if base, ok := viFold[r]; ok {
			b.WriteRune(base)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// MaxItemNameRunes giới hạn tên món đọc từ hóa đơn.
const MaxItemNameRunes = 120

// CleanItemName làm sạch tên món do AI đọc: bỏ ký tự điều khiển, gộp khoảng trắng,
// cắt độ dài. Tên món là dữ liệu, không bao giờ được dùng làm chỉ dẫn cho model.
func CleanItemName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxItemNameRunes {
		s = string([]rune(s)[:MaxItemNameRunes])
	}
	return s
}

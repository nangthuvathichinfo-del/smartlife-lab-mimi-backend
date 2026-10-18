// Package apperr định nghĩa lỗi nghiệp vụ có mã ổn định. Mã lỗi là một phần của hợp
// đồng API (api/openapi.yaml): client dựa vào Code để hiện đúng câu tiếng Việt và
// quyết định có cho thử lại không. Message chỉ là gợi ý, không chứa chi tiết nội bộ.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Code string

const (
	CodeBadRequest        Code = "bad_request"
	CodeValidation        Code = "validation_failed"
	CodeUnauthenticated   Code = "unauthenticated"
	CodeAppCheck          Code = "app_check_failed"
	CodeForbidden         Code = "forbidden"
	CodeNotFound          Code = "not_found"
	CodeConflict          Code = "conflict"
	CodeReceiptConflict   Code = "receipt_conflict"
	CodePossibleDuplicate Code = "possible_duplicate"
	CodePayloadTooLarge   Code = "payload_too_large"
	CodeUnsupportedMedia  Code = "unsupported_media"
	CodeQuotaExceeded     Code = "quota_exceeded"
	CodeRateLimited       Code = "rate_limited"
	CodeReceiptUnreadable Code = "receipt_unreadable"
	CodeAIUnavailable     Code = "ai_unavailable"
	CodeNotImplemented    Code = "not_implemented"
	CodeInternal          Code = "internal"
)

var statusByCode = map[Code]int{
	CodeBadRequest:        http.StatusBadRequest,
	CodeValidation:        http.StatusUnprocessableEntity,
	CodeUnauthenticated:   http.StatusUnauthorized,
	CodeAppCheck:          http.StatusUnauthorized,
	CodeForbidden:         http.StatusForbidden,
	CodeNotFound:          http.StatusNotFound,
	CodeConflict:          http.StatusConflict,
	CodeReceiptConflict:   http.StatusConflict,
	CodePossibleDuplicate: http.StatusConflict,
	CodePayloadTooLarge:   http.StatusRequestEntityTooLarge,
	CodeUnsupportedMedia:  http.StatusUnsupportedMediaType,
	CodeQuotaExceeded:     http.StatusTooManyRequests,
	CodeRateLimited:       http.StatusTooManyRequests,
	CodeReceiptUnreadable: http.StatusUnprocessableEntity,
	CodeAIUnavailable:     http.StatusServiceUnavailable,
	CodeNotImplemented:    http.StatusNotImplemented,
	CodeInternal:          http.StatusInternalServerError,
}

// Error là lỗi có thể trả thẳng cho client.
type Error struct {
	Code    Code
	Message string         // câu tiếng Việt ngắn, an toàn để hiển thị
	Fields  map[string]any // chi tiết có cấu trúc, ví dụ {"reset_at": "..."} hoặc lỗi theo trường
	Err     error          // nguyên nhân gốc, chỉ để log, không bao giờ gửi cho client
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Status trả về mã HTTP tương ứng.
func (e *Error) Status() int {
	if s, ok := statusByCode[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func New(code Code, msg string) *Error { return &Error{Code: code, Message: msg} }

func Wrap(code Code, msg string, err error) *Error { return &Error{Code: code, Message: msg, Err: err} }

func (e *Error) With(key string, v any) *Error {
	cp := *e
	cp.Fields = make(map[string]any, len(e.Fields)+1)
	for k, val := range e.Fields {
		cp.Fields[k] = val
	}
	cp.Fields[key] = v
	return &cp
}

// From chuyển lỗi bất kỳ thành *Error. Lỗi không rõ nguồn gốc thành "internal" và
// thông điệp chung; chi tiết nằm trong Err để log.
func From(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return &Error{Code: CodeInternal, Message: "Có lỗi xảy ra, thử lại sau ít phút.", Err: err}
}

// Các lỗi dùng nhiều.
var (
	ErrUnauthenticated = New(CodeUnauthenticated, "Phiên đăng nhập đã hết, hãy đăng nhập lại.")
	ErrForbidden       = New(CodeForbidden, "Bạn không có quyền với mục này.")
	ErrNotFound        = New(CodeNotFound, "Không tìm thấy.")
)

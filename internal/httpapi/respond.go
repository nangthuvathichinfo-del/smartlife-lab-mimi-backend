package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
)

type errorBody struct {
	Code    apperr.Code    `json:"code"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // dữ liệu cá nhân, không để CDN hay proxy giữ
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeError là nơi duy nhất biến lỗi thành response. Lỗi 5xx được log kèm nguyên nhân
// gốc; client chỉ thấy mã và câu ngắn.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.From(err)
	status := ae.Status()
	if status >= 500 {
		s.log.ErrorContext(r.Context(), "request failed", "code", ae.Code, "err", err)
	} else {
		s.log.InfoContext(r.Context(), "request rejected", "code", ae.Code)
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
	}
	writeJSON(w, status, errorBody{Code: ae.Code, Message: ae.Message, Fields: ae.Fields})
}

// decodeJSON đọc đúng một object JSON, từ chối trường lạ (chống ghi đè trường không
// được phép) và giới hạn kích thước.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apperr.New(apperr.CodePayloadTooLarge, "Dữ liệu gửi lên quá lớn.")
		}
		return apperr.Wrap(apperr.CodeBadRequest, "Dữ liệu gửi lên không đúng định dạng.", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apperr.New(apperr.CodeBadRequest, "Chỉ gửi một đối tượng JSON.")
	}
	return nil
}

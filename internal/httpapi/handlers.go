package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

const (
	maxJSONBody    = 256 << 10 // 256 KiB cho JSON thường
	maxUploadBody  = 30 << 20  // dưới giới hạn request 32 MiB của Cloud Run
	maxImageBytes  = 4 << 20
	maxImagesCount = 8
)

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz kiểm tra Firestore bằng một lần đọc nhỏ; không gọi Gemini (tốn tiền và lỗi
// bên ngoài không nên làm Cloud Run tưởng instance hỏng).
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.ready.Ready(ctx); err != nil {
		s.log.WarnContext(ctx, "not ready", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	prof, err := s.profiles.Profile(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

// readReceipt nhận multipart: receipt_id, lang, images (1..8 ảnh). Đọc từng phần thẳng
// vào bộ nhớ bằng MultipartReader, KHÔNG dùng ParseMultipartForm vì hàm đó ghi phần
// lớn ra file tạm trên đĩa, trái nguyên tắc không lưu ảnh.
func (s *Server) readReceipt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		s.writeError(w, r, apperr.New(apperr.CodeBadRequest, "Cần gửi ảnh dạng multipart."))
		return
	}
	var receiptID, lang string
	var imgs []receipt.Image
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.writeError(w, r, uploadErr(err))
			return
		}
		switch part.FormName() {
		case "receipt_id", "lang":
			b, err := io.ReadAll(io.LimitReader(part, 128))
			if err != nil {
				s.writeError(w, r, uploadErr(err))
				return
			}
			if part.FormName() == "receipt_id" {
				receiptID = string(bytes.TrimSpace(b))
			} else {
				lang = string(bytes.TrimSpace(b))
			}
		case "images":
			if len(imgs) == maxImagesCount {
				s.writeError(w, r, apperr.New(apperr.CodeValidation, "Mỗi hóa đơn tối đa 8 ảnh."))
				return
			}
			data, err := io.ReadAll(io.LimitReader(part, maxImageBytes+1))
			if err != nil {
				s.writeError(w, r, uploadErr(err))
				return
			}
			mt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			imgs = append(imgs, receipt.Image{Data: data, MIME: mt}) // service kiểm tra byte đầu file
		}
		_ = part.Close()
	}
	if lang != "en" {
		lang = "vi"
	}
	d, err := s.receipts.Read(r.Context(), principalFrom(r.Context()), receiptID, lang, imgs)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func uploadErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.New(apperr.CodePayloadTooLarge, "Ảnh gửi lên quá lớn.")
	}
	return apperr.Wrap(apperr.CodeBadRequest, "Không đọc được dữ liệu ảnh.", err)
}

func (s *Server) saveReceipt(w http.ResponseWriter, r *http.Request) {
	var req receipt.SaveRequest
	if err := decodeJSON(w, r, &req, maxJSONBody); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.receipts.Save(r.Context(), principalFrom(r.Context()), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"receipt_id": out.ReceiptID, "status": "saved", "replayed": out.Replayed})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	horizon, _ := strconv.Atoi(q.Get("horizon"))
	if horizon == 0 {
		horizon = 7
	}
	tab := q.Get("tab")
	if tab == "" {
		tab = "active"
	}
	items, err := s.items.List(r.Context(), principalFrom(r.Context()), item.Query{Tab: tab, Horizon: horizon, Search: q.Get("q")})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listSharedNeeds(w http.ResponseWriter, r *http.Request) {
	horizon, _ := strconv.Atoi(r.URL.Query().Get("horizon"))
	if horizon != 3 && horizon != 14 {
		horizon = 7
	}
	items, err := s.items.ListShared(r.Context(), principalFrom(r.Context()), horizon)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) changeStock(w http.ResponseWriter, r *http.Request) {
	var c item.StockChange
	if err := decodeJSON(w, r, &c, maxJSONBody); err != nil {
		s.writeError(w, r, err)
		return
	}
	it, err := s.items.ChangeStock(r.Context(), principalFrom(r.Context()), r.PathValue("key"), c)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) undoStock(w http.ResponseWriter, r *http.Request) {
	actedAt, err := time.Parse(time.RFC3339, r.URL.Query().Get("acted_at"))
	if err != nil {
		s.writeError(w, r, apperr.New(apperr.CodeValidation, "Thiếu thời điểm thao tác.").With("field", "acted_at"))
		return
	}
	it, err := s.items.UndoStock(r.Context(), principalFrom(r.Context()), r.PathValue("key"), actedAt)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	d, err := s.items.Get(r.Context(), principalFrom(r.Context()), r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) getHome(w http.ResponseWriter, r *http.Request) {
	sum, err := s.home.Get(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) getSpending(w http.ResponseWriter, r *http.Request) {
	rep, err := s.spending.Get(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("month"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var patch user.SettingsPatch
	if err := decodeJSON(w, r, &patch, maxJSONBody); err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.settings.Update(r.Context(), principalFrom(r.Context()), patch)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	cart, err := s.plan.Get(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

// addPlanItem: PUT không có thân; khóa nhóm nằm trên đường dẫn. Trả giỏ sau khi thêm.
func (s *Server) addPlanItem(w http.ResponseWriter, r *http.Request) {
	cart, err := s.plan.Add(r.Context(), principalFrom(r.Context()), r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

func (s *Server) removePlanItem(w http.ResponseWriter, r *http.Request) {
	if err := s.plan.Remove(r.Context(), principalFrom(r.Context()), r.PathValue("key")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putPlanState(w http.ResponseWriter, r *http.Request) {
	var st plan.State
	if err := decodeJSON(w, r, &st, maxJSONBody); err != nil {
		s.writeError(w, r, err)
		return
	}
	saved, err := s.plan.SetState(r.Context(), principalFrom(r.Context()), st)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) getPlanPrices(w http.ResponseWriter, r *http.Request) {
	pr, err := s.plan.Prices(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := s.notes.List(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, apperr.New(apperr.CodeNotImplemented, "Chức năng này chưa có trên máy chủ."))
}

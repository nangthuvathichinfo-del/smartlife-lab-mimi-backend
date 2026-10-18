package receipt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

// ---- fakes: hiện thực tối thiểu các interface, không mock thư viện ----

type fakeReader struct {
	mu    sync.Mutex
	calls int
	fn    func(call int) (Page, error)
}

func (f *fakeReader) ReadPage(ctx context.Context, img Image, lang string) (Page, error) {
	f.mu.Lock()
	f.calls++
	c := f.calls
	f.mu.Unlock()
	return f.fn(c)
}

type fakeDrafts struct{ m map[string]Draft }

func (f *fakeDrafts) Get(_ context.Context, uid, id string) (*Draft, error) {
	if d, ok := f.m[uid+id]; ok {
		return &d, nil
	}
	return nil, nil
}
func (f *fakeDrafts) Put(_ context.Context, uid string, d Draft) error {
	f.m[uid+d.ReceiptID] = d
	return nil
}

type fakeQuota struct{ used, released int }

func (f *fakeQuota) Reserve(_ context.Context, uid, day string, limit int) error {
	if f.used >= limit {
		return apperr.New(apperr.CodeQuotaExceeded, "hết lượt")
	}
	f.used++
	return nil
}
func (f *fakeQuota) Release(_ context.Context, uid, day string) error {
	f.used--
	f.released++
	return nil
}

type fakeFX struct{ rates map[string]string }

func (f fakeFX) Rate(_ context.Context, cur string, _ domain.Date) (FX, bool, error) {
	r, ok := f.rates[cur]
	return FX{Rate: r, Source: "test"}, ok, nil
}

type fakeStore struct {
	saved   map[string]Receipt
	similar bool
}

func (f *fakeStore) SaveReceipt(_ context.Context, uid string, r Receipt, _ *time.Location) (SaveOutcome, error) {
	if old, ok := f.saved[r.ID]; ok {
		if old.PayloadHash != r.PayloadHash {
			return SaveOutcome{}, apperr.New(apperr.CodeReceiptConflict, "khác nội dung")
		}
		return SaveOutcome{ReceiptID: r.ID, Replayed: true}, nil
	}
	f.saved[r.ID] = r
	return SaveOutcome{ReceiptID: r.ID}, nil
}
func (f *fakeStore) HasSimilar(context.Context, string, string, string) (bool, error) {
	return f.similar, nil
}

var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}
var owner = authz.Principal{UID: "u1", Role: authz.RoleOwner, Timezone: "Asia/Ho_Chi_Minh"}

func newSvc(t *testing.T, r Reader, q *fakeQuota, st *fakeStore) *Service {
	t.Helper()
	fixed := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	s, err := New(r, &fakeDrafts{m: map[string]Draft{}}, q, fakeFX{rates: map[string]string{"SGD": "18450.25"}}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithClock(func() time.Time { return fixed }),
		WithTaxonomy([]string{"sua", "hoa-pham"}),
		WithPageTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func code(err error) apperr.Code {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestRead(t *testing.T) {
	page := Page{Store: "Co.opmart", PurchasedOn: "2026-10-15", TotalPrinted: "102.000", Lines: []PageLine{
		{Name: "Sữa tươi", Qty: 2, Amount: "64.000", CategoryKey: "sua"},
		{Name: "Nước rửa chén", Qty: 1, Amount: "38,000", CategoryKey: "khong-co"},
	}}

	t.Run("parses VND, checks total, drops unknown category", func(t *testing.T) {
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return page, nil }}, &fakeQuota{}, &fakeStore{})
		d, err := s.Read(context.Background(), owner, "r-1", "vi", []Image{{jpeg, "image/jpeg"}})
		if err != nil {
			t.Fatal(err)
		}
		if d.SumVND != 102000 || d.TotalMismatch {
			t.Fatalf("sum %d mismatch %v", d.SumVND, d.TotalMismatch)
		}
		if d.Lines[1].CategoryKey != "" {
			t.Fatalf("unknown category kept: %q", d.Lines[1].CategoryKey)
		}
	})

	t.Run("replay returns cached draft without calling AI or quota", func(t *testing.T) {
		r := &fakeReader{fn: func(int) (Page, error) { return page, nil }}
		q := &fakeQuota{}
		s := newSvc(t, r, q, &fakeStore{})
		for i := 0; i < 2; i++ {
			if _, err := s.Read(context.Background(), owner, "r-2", "vi", []Image{{jpeg, "image/jpeg"}}); err != nil {
				t.Fatal(err)
			}
		}
		if r.calls != 1 || q.used != 1 {
			t.Fatalf("calls=%d used=%d", r.calls, q.used)
		}
	})

	t.Run("transient failure retried once then succeeds", func(t *testing.T) {
		r := &fakeReader{fn: func(c int) (Page, error) {
			if c == 1 {
				return Page{}, fmt.Errorf("429: %w", ErrTransient)
			}
			return page, nil
		}}
		s := newSvc(t, r, &fakeQuota{}, &fakeStore{})
		if _, err := s.Read(context.Background(), owner, "r-3", "vi", []Image{{jpeg, "image/jpeg"}}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("AI down releases quota", func(t *testing.T) {
		q := &fakeQuota{}
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return Page{}, ErrTransient }}, q, &fakeStore{})
		_, err := s.Read(context.Background(), owner, "r-4", "vi", []Image{{jpeg, "image/jpeg"}})
		if code(err) != apperr.CodeAIUnavailable || q.released != 1 || q.used != 0 {
			t.Fatalf("err=%v released=%d used=%d", err, q.released, q.used)
		}
	})

	t.Run("unreadable is 422 and frees quota", func(t *testing.T) {
		q := &fakeQuota{}
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return Page{}, ErrUnreadable }}, q, &fakeStore{})
		_, err := s.Read(context.Background(), owner, "r-5", "vi", []Image{{jpeg, "image/jpeg"}})
		if code(err) != apperr.CodeReceiptUnreadable || q.used != 0 {
			t.Fatalf("err=%v used=%d", err, q.used)
		}
	})

	t.Run("content type not trusted", func(t *testing.T) {
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return page, nil }}, &fakeQuota{}, &fakeStore{})
		_, err := s.Read(context.Background(), owner, "r-6", "vi", []Image{{[]byte("<svg>"), "image/jpeg"}})
		if code(err) != apperr.CodeUnsupportedMedia {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("foreign currency without rate is flagged, not guessed", func(t *testing.T) {
		pg := page
		pg.Currency = "JPY"
		pg.TotalPrinted = ""
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return pg, nil }}, &fakeQuota{}, &fakeStore{})
		d, err := s.Read(context.Background(), owner, "r-7", "vi", []Image{{jpeg, "image/jpeg"}})
		if err != nil || !d.FXMissing || d.Lines[0].AmountVND != nil || d.SumVND != 0 {
			t.Fatalf("err=%v draft=%+v", err, d)
		}
	})

	t.Run("member cannot read receipts", func(t *testing.T) {
		s := newSvc(t, &fakeReader{fn: func(int) (Page, error) { return page, nil }}, &fakeQuota{}, &fakeStore{})
		_, err := s.Read(context.Background(), authz.Principal{UID: "m", Role: authz.RoleMember}, "r-8", "vi", []Image{{jpeg, "image/jpeg"}})
		if code(err) != apperr.CodeForbidden {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestSave(t *testing.T) {
	req := SaveRequest{
		ReceiptID: "r-10", Store: "Co.opmart", PurchasedOn: "2026-10-15", Currency: "VND",
		UseForPrediction: true,
		Lines: []SaveLine{
			{LineID: "l1", Name: "Sữa tươi", Qty: 2, Amount: "64000", CategoryKey: "sua", Habit: true},
			{LineID: "l2", Name: "Bánh quy", Qty: 1, Amount: "74000", Habit: false},
		},
	}

	t.Run("server recomputes totals and replay is idempotent", func(t *testing.T) {
		st := &fakeStore{saved: map[string]Receipt{}}
		s := newSvc(t, nil, &fakeQuota{}, st)
		out, err := s.Save(context.Background(), owner, req)
		if err != nil || out.Replayed {
			t.Fatalf("first save: %v %+v", err, out)
		}
		if st.saved["r-10"].TotalVND != 138000 || st.saved["r-10"].Lines[1].Habit {
			t.Fatalf("saved %+v", st.saved["r-10"])
		}
		out, err = s.Save(context.Background(), owner, req)
		if err != nil || !out.Replayed {
			t.Fatalf("replay: %v %+v", err, out)
		}
	})

	t.Run("same id different content conflicts", func(t *testing.T) {
		st := &fakeStore{saved: map[string]Receipt{}}
		s := newSvc(t, nil, &fakeQuota{}, st)
		if _, err := s.Save(context.Background(), owner, req); err != nil {
			t.Fatal(err)
		}
		changed := req
		changed.Lines = append([]SaveLine(nil), req.Lines...)
		changed.Lines[0].Amount = "1"
		if _, err := s.Save(context.Background(), owner, changed); code(err) != apperr.CodeReceiptConflict {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("possible duplicate asks for confirmation", func(t *testing.T) {
		st := &fakeStore{saved: map[string]Receipt{}, similar: true}
		s := newSvc(t, nil, &fakeQuota{}, st)
		if _, err := s.Save(context.Background(), owner, req); code(err) != apperr.CodePossibleDuplicate {
			t.Fatalf("err=%v", err)
		}
		confirmed := req
		confirmed.ConfirmDuplicate = true
		if _, err := s.Save(context.Background(), owner, confirmed); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("prediction switch off marks every line habit=false", func(t *testing.T) {
		st := &fakeStore{saved: map[string]Receipt{}}
		s := newSvc(t, nil, &fakeQuota{}, st)
		off := req
		off.ReceiptID = "r-11"
		off.UseForPrediction = false
		if _, err := s.Save(context.Background(), owner, off); err != nil {
			t.Fatal(err)
		}
		if st.saved["r-11"].Lines[0].Habit {
			t.Fatal("habit should be false")
		}
	})

	t.Run("validation", func(t *testing.T) {
		s := newSvc(t, nil, &fakeQuota{}, &fakeStore{saved: map[string]Receipt{}})
		bad := []func(*SaveRequest){
			func(r *SaveRequest) { r.ReceiptID = "../x" },
			func(r *SaveRequest) { r.PurchasedOn = "2027-01-01" }, // tương lai
			func(r *SaveRequest) { r.Lines = nil },
			func(r *SaveRequest) { r.Lines = []SaveLine{{Name: "x", Qty: 1, Amount: "-5"}} },
			func(r *SaveRequest) { r.Lines = []SaveLine{{Name: "x", Qty: 0, Amount: "5"}} },
		}
		for i, mut := range bad {
			r := req
			r.ReceiptID = fmt.Sprintf("r-v%d", i)
			mut(&r)
			if _, err := s.Save(context.Background(), owner, r); code(err) != apperr.CodeValidation {
				t.Errorf("case %d: err=%v", i, err)
			}
		}
	})
}

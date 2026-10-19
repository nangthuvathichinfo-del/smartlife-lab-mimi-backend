package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/home"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
	"github.com/smartlife-lab/mimi/backend/internal/spending"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

type fakeTokens map[string]string // token -> uid

func (f fakeTokens) VerifyIDToken(_ context.Context, tok string) (string, error) {
	if uid, ok := f[tok]; ok {
		return uid, nil
	}
	return "", errors.New("invalid token")
}

type fakeAppCheck struct{}

func (fakeAppCheck) VerifyAppCheck(_ context.Context, tok string) error {
	if tok != "ok" {
		return errors.New("bad app check")
	}
	return nil
}

type fakePrincipals map[string]authz.Principal

func (f fakePrincipals) LoadPrincipal(_ context.Context, uid string) (authz.Principal, error) {
	return f[uid], nil
}

type fakeProfiles struct{}

func (fakeProfiles) Profile(_ context.Context, p authz.Principal) (user.Profile, error) {
	return user.Profile{UID: p.UID, Role: p.Role.String()}, nil
}

type fakeReceipts struct{ saved int }

func (f *fakeReceipts) Read(context.Context, authz.Principal, string, string, []receipt.Image) (receipt.Draft, error) {
	return receipt.Draft{}, nil
}
func (f *fakeReceipts) Save(_ context.Context, _ authz.Principal, req receipt.SaveRequest) (receipt.SaveOutcome, error) {
	f.saved++
	return receipt.SaveOutcome{ReceiptID: req.ReceiptID}, nil
}

type fakeItems struct{}

func (fakeItems) List(_ context.Context, p authz.Principal, _ item.Query) ([]item.Item, error) {
	if err := authz.Can(p, authz.ActionReadItemsFull); err != nil {
		return nil, err
	}
	return []item.Item{{Key: "sua"}}, nil
}
func (fakeItems) Get(_ context.Context, p authz.Principal, key string) (item.Detail, error) {
	if err := authz.Can(p, authz.ActionReadItemsFull); err != nil {
		return item.Detail{}, err
	}
	if key != "sua" {
		return item.Detail{}, apperr.ErrNotFound
	}
	return item.Detail{Item: item.Item{Key: "sua", Name: "Sữa"}, History: []item.HistoryEntry{}}, nil
}
func (fakeItems) ListShared(context.Context, authz.Principal, int) ([]item.SharedItem, error) {
	return nil, nil
}
func (fakeItems) ChangeStock(context.Context, authz.Principal, string, item.StockChange) (item.Item, error) {
	return item.Item{}, nil
}
func (fakeItems) UndoStock(context.Context, authz.Principal, string, time.Time) (item.Item, error) {
	return item.Item{}, nil
}

type fakeHome struct{}

func (fakeHome) Get(_ context.Context, p authz.Principal) (home.Summary, error) {
	return home.Summary{NeedsCount: 2}, nil
}

type fakeSpendingStore struct{}

func (fakeSpendingStore) Month(context.Context, string, string) (spending.Month, error) {
	return spending.Month{SpentVND: 500}, nil
}
func (fakeSpendingStore) MonthReceipts(context.Context, string, string, int) ([]spending.ReceiptRow, error) {
	return nil, nil
}

type fakeSettingsStore struct{}

func (fakeSettingsStore) UpdateSettings(_ context.Context, _ string, apply func(user.Stored) (user.Stored, error)) (user.Stored, error) {
	return apply(user.Stored{DisplayName: "Lan", Settings: map[string]any{"lang": "vi"}})
}

// fakePlanStore + fakePlanItems: dùng plan.Service thật để test cả kiểm tra đầu vào.
type fakePlanStore struct{ lines map[string]plan.Line }

func (f *fakePlanStore) OpenLines(context.Context, string) ([]plan.Line, error) {
	var out []plan.Line
	for _, l := range f.lines {
		out = append(out, l)
	}
	return out, nil
}
func (f *fakePlanStore) AddLine(_ context.Context, _ string, l plan.Line) error {
	f.lines[l.Key] = l
	return nil
}
func (f *fakePlanStore) RemoveLine(_ context.Context, _ string, k string) error {
	delete(f.lines, k)
	return nil
}
func (f *fakePlanStore) State(context.Context, string) (plan.State, error)  { return plan.State{}, nil }
func (f *fakePlanStore) SetState(context.Context, string, plan.State) error { return nil }
func (f *fakePlanStore) Prices(context.Context, string, []string) ([]domain.LastPrice, error) {
	return []domain.LastPrice{{GroupKey: "sua", Store: "Co.op", Unit: 32000}}, nil
}

type fakePlanItems struct{}

func (fakePlanItems) GetItem(_ context.Context, _ string, k string) (item.Record, error) {
	if k != "sua" {
		return item.Record{}, apperr.ErrNotFound
	}
	return item.Record{GroupKey: "sua", Name: "Sữa"}, nil
}
func (fakePlanItems) ListItems(context.Context, string) ([]item.Record, error) {
	return []item.Record{{GroupKey: "sua", Name: "Sữa"}}, nil
}

type fakeReady struct{}

func (fakeReady) Ready(context.Context) error { return nil }

func newTestServer(t *testing.T) (http.Handler, *fakeReceipts) {
	t.Helper()
	rc := &fakeReceipts{}
	s, err := New(Deps{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tokens:   fakeTokens{"owner-tok": "u1", "member-tok": "u2"},
		AppCheck: fakeAppCheck{},
		Principals: fakePrincipals{
			"u1": {UID: "u1", Role: authz.RoleOwner},
			"u2": {UID: "u2", Role: authz.RoleMember, HouseholdID: "h1"},
		},
		Profiles: fakeProfiles{}, Receipts: rc, Items: fakeItems{}, Ready: fakeReady{},
		Home: fakeHome{}, Spending: spending.New(fakeSpendingStore{}), Settings: user.NewSettingsService(fakeSettingsStore{}, nil),
		Plan: plan.New(&fakePlanStore{lines: map[string]plan.Line{}}, fakePlanItems{}, nil),
	}, WithWriteLimit(2))
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), rc
}

func do(h http.Handler, method, path, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Firebase-AppCheck", "ok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func TestServer(t *testing.T) {
	h, rc := newTestServer(t)

	t.Run("health is public", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/health", "/api/health"} {
			if rec, _ := do(h, "GET", p, "", ""); rec.Code != 200 {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
		}
	})
	t.Run("missing token is 401 with stable code", func(t *testing.T) {
		rec, body := do(h, "GET", "/api/items", "", "")
		if rec.Code != 401 || body["code"] != "unauthenticated" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
	t.Run("app check enforced", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/items", nil)
		req.Header.Set("Authorization", "Bearer owner-tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "app_check_failed") {
			t.Fatalf("status %d body %s", rec.Code, rec.Body)
		}
	})
	t.Run("works with and without /api prefix", func(t *testing.T) {
		for _, p := range []string{"/items", "/api/items"} {
			if rec, _ := do(h, "GET", p, "owner-tok", ""); rec.Code != 200 {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
		}
	})
	t.Run("member is forbidden from full items", func(t *testing.T) {
		rec, body := do(h, "GET", "/items", "member-tok", "")
		if rec.Code != 403 || body["code"] != "forbidden" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
	t.Run("unknown JSON field rejected", func(t *testing.T) {
		rec, body := do(h, "POST", "/receipts", "owner-tok", `{"receipt_id":"r-1","total_vnd":1}`)
		if rec.Code != 400 || body["code"] != "bad_request" || rc.saved != 0 {
			t.Fatalf("status %d body %v saved %d", rec.Code, body, rc.saved)
		}
	})
	t.Run("write rate limit per user", func(t *testing.T) {
		h2, _ := newTestServer(t)
		codes := []int{}
		for i := 0; i < 3; i++ {
			rec, _ := do(h2, "POST", "/receipts", "owner-tok", `{"receipt_id":"r-1"}`)
			codes = append(codes, rec.Code)
		}
		if codes[2] != 429 {
			t.Fatalf("codes %v", codes)
		}
	})
	t.Run("home summary", func(t *testing.T) {
		rec, body := do(h, "GET", "/api/home", "owner-tok", "")
		if rec.Code != 200 || body["needs_count"] != float64(2) || body["spent_this_month"] != nil {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
	t.Run("item detail", func(t *testing.T) {
		rec, body := do(h, "GET", "/api/items/sua", "owner-tok", "")
		if rec.Code != 200 || body["key"] != "sua" || body["history"] == nil {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
		if rec, _ := do(h, "GET", "/api/items/khong-co", "owner-tok", ""); rec.Code != 404 {
			t.Fatalf("missing item status %d", rec.Code)
		}
		if rec, _ := do(h, "GET", "/api/items/sua", "member-tok", ""); rec.Code != 403 {
			t.Fatalf("member status %d", rec.Code)
		}
	})
	t.Run("spending", func(t *testing.T) {
		rec, body := do(h, "GET", "/api/spending?month=2026-10", "owner-tok", "")
		if rec.Code != 200 || body["spent"] != float64(500) || body["month"] != "2026-10" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
		if rec, body := do(h, "GET", "/api/spending?month=10-2026", "owner-tok", ""); rec.Code != 422 || body["code"] != "validation_failed" {
			t.Fatalf("bad month status %d body %v", rec.Code, body)
		}
		if rec, _ := do(h, "GET", "/api/spending?month=2026-10", "member-tok", ""); rec.Code != 403 {
			t.Fatalf("member status %d", rec.Code)
		}
	})
	t.Run("settings", func(t *testing.T) {
		h3, _ := newTestServer(t) // bộ đếm ghi riêng
		rec, body := do(h3, "PUT", "/api/settings", "owner-tok", `{"lead_days":5,"lang":"en"}`)
		if rec.Code != 200 || body["lead_days"] != float64(5) || body["lang"] != "en" || body["display_name"] != "Lan" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
		if rec, body := do(h3, "PUT", "/api/settings", "owner-tok", `{"lead_days":99}`); rec.Code != 422 || body["code"] != "validation_failed" {
			t.Fatalf("invalid status %d body %v", rec.Code, body)
		}
	})
	t.Run("unknown settings field rejected", func(t *testing.T) {
		h4, _ := newTestServer(t)
		if rec, body := do(h4, "PUT", "/api/settings", "owner-tok", `{"role":"owner"}`); rec.Code != 400 || body["code"] != "bad_request" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
	t.Run("stock change and undo accept browser timestamps", func(t *testing.T) {
		h5, _ := newTestServer(t)
		// new Date().toISOString() có phần mili giây và đuôi Z.
		if rec, body := do(h5, "PATCH", "/api/items/sua/stock", "owner-tok", `{"quick":"out","acted_at":"2026-10-18T09:00:00.123Z"}`); rec.Code != 200 {
			t.Fatalf("stock status %d body %v", rec.Code, body)
		}
		if rec, body := do(h5, "DELETE", "/api/items/sua/override?acted_at=2026-10-18T09%3A00%3A05.456Z", "owner-tok", ""); rec.Code != 200 {
			t.Fatalf("undo status %d body %v", rec.Code, body)
		}
	})
	t.Run("save accepts exactly what the web client sends", func(t *testing.T) {
		h6, rc6 := newTestServer(t)
		// Dạng do toSaveRequest (mimi-web/src/api/hooks.ts) dựng từ bản nháp.
		body := `{"receipt_id":"7f0c2a9e-1b2c-4d5e-8f90-123456789abc","store":"Co.opmart","purchased_on":"2026-10-16",
			"currency":"VND","discount_total":"","lines":[{"line_id":"l1","name":"Sữa tươi 1L","qty":2,"amount":"64000",
			"category_key":"sua","group_key":"","habit":true}],"use_for_prediction":true,"confirm_duplicate":false}`
		if rec, b := do(h6, "POST", "/api/receipts", "owner-tok", body); rec.Code != 201 || rc6.saved != 1 {
			t.Fatalf("status %d body %v", rec.Code, b)
		}
		// Gửi kèm trường của bản nháp (sum_vnd) như bản cũ thì bị từ chối.
		bad := `{"receipt_id":"r-2","purchased_on":"2026-10-16","lines":[],"sum_vnd":1}`
		if rec, _ := do(h6, "POST", "/api/receipts", "owner-tok", bad); rec.Code != 400 {
			t.Fatalf("draft-shaped body status %d", rec.Code)
		}
	})
	t.Run("shopping plan", func(t *testing.T) {
		h7, _ := newTestServer(t)
		if rec, body := do(h7, "GET", "/api/plan", "owner-tok", ""); rec.Code != 200 || body["item_count"] != float64(0) {
			t.Fatalf("empty plan status %d body %v", rec.Code, body)
		}
		rec, body := do(h7, "PUT", "/api/plan/items/sua", "owner-tok", "")
		if rec.Code != 200 || body["item_count"] != float64(1) || body["estimate"] != float64(32000) {
			t.Fatalf("add status %d body %v", rec.Code, body)
		}
		if rec, _ := do(h7, "GET", "/api/plan/prices", "owner-tok", ""); rec.Code != 200 {
			t.Fatalf("prices status %d", rec.Code)
		}
		h8, _ := newTestServer(t) // bộ đếm ghi riêng (giới hạn 2 lần ghi mỗi server test)
		if rec, _ := do(h8, "PUT", "/api/plan/items/khong-co", "owner-tok", ""); rec.Code != 404 {
			t.Fatalf("unknown item status %d", rec.Code)
		}
		if rec, body := do(h8, "PUT", "/api/plan/state", "owner-tok", `{"buy_date":"","buy_hour":25,"trip_note":""}`); rec.Code != 422 {
			t.Fatalf("bad state status %d body %v", rec.Code, body)
		}
		h9, _ := newTestServer(t)
		if rec, _ := do(h9, "DELETE", "/api/plan/items/sua", "owner-tok", ""); rec.Code != 204 {
			t.Fatalf("remove status %d", rec.Code)
		}
		if rec, _ := do(h9, "GET", "/api/plan", "member-tok", ""); rec.Code != 403 {
			t.Fatalf("member status %d", rec.Code)
		}
	})
	t.Run("planned endpoints answer 501", func(t *testing.T) {
		rec, body := do(h, "GET", "/forecast", "owner-tok", "")
		if rec.Code != 501 || body["code"] != "not_implemented" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
}

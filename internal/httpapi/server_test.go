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

	"github.com/smartlife-lab/mimi/backend/internal/authz"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
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
func (fakeItems) ListShared(context.Context, authz.Principal, int) ([]item.SharedItem, error) {
	return nil, nil
}
func (fakeItems) ChangeStock(context.Context, authz.Principal, string, item.StockChange) (item.Item, error) {
	return item.Item{}, nil
}
func (fakeItems) UndoStock(context.Context, authz.Principal, string, time.Time) (item.Item, error) {
	return item.Item{}, nil
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
		if rec, _ := do(h, "GET", "/healthz", "", ""); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
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
	t.Run("planned endpoints answer 501", func(t *testing.T) {
		rec, body := do(h, "GET", "/spending", "owner-tok", "")
		if rec.Code != 501 || body["code"] != "not_implemented" {
			t.Fatalf("status %d body %v", rec.Code, body)
		}
	})
}

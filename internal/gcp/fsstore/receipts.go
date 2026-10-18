package fsstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/smartlife-lab/mimi/backend/internal/apperr"
	"github.com/smartlife-lab/mimi/backend/internal/domain"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
)

var (
	_ receipt.Store      = (*Store)(nil)
	_ receipt.DraftCache = (*Store)(nil)
	_ receipt.Quota      = (*Store)(nil)
	_ receipt.FXRates    = (*Store)(nil)
)

// ---------- Bản nháp (TTL 1 giờ, không có ảnh) ----------

func (s *Store) Get(ctx context.Context, uid, receiptID string) (*receipt.Draft, error) {
	snap, err := s.user(uid).Collection("drafts").Doc(receiptID).Get(ctx)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, _ := snap.Data()["json"].(string)
	var d receipt.Draft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) Put(ctx context.Context, uid string, d receipt.Draft) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.user(uid).Collection("drafts").Doc(d.ReceiptID).Set(ctx, map[string]any{
		"json": string(b), "expire_at": time.Now().Add(time.Hour),
	})
	return err
}

// ---------- Tỷ giá ----------

// Rate lấy tỷ giá gần nhất không sau ngày mua, do job fx-sync ghi (có nguồn và thời điểm).
func (s *Store) Rate(ctx context.Context, currency string, on domain.Date) (receipt.FX, bool, error) {
	docs, err := s.c.Collection("fx_rates").
		Where("currency", "==", currency).
		Where("on", "<=", on.String()).
		OrderBy("on", firestore.Desc).Limit(1).
		Documents(ctx).GetAll()
	if err != nil || len(docs) == 0 {
		return receipt.FX{}, false, err
	}
	var v struct {
		Rate      string    `firestore:"rate"`
		Source    string    `firestore:"source"`
		FetchedAt time.Time `firestore:"fetched_at"`
		Stale     bool      `firestore:"stale"`
	}
	if err := docs[0].DataTo(&v); err != nil {
		return receipt.FX{}, false, err
	}
	return receipt.FX{Rate: v.Rate, Source: v.Source, At: v.FetchedAt}, true, nil
}

// ---------- Cảnh báo trùng ----------

func (s *Store) HasSimilar(ctx context.Context, uid, key, exceptID string) (bool, error) {
	docs, err := s.user(uid).Collection("receipts").Where("dedupe_key", "==", key).Limit(2).Documents(ctx).GetAll()
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		if d.Ref.ID != exceptID {
			return true, nil
		}
	}
	return false, nil
}

// ---------- Lưu hóa đơn: một transaction ----------

// purchaseHistory: số lần mua gần nhất cần đọc cho mỗi nhóm (đủ cho trung vị 3 khoảng).
const purchaseHistory = 6

func (s *Store) SaveReceipt(ctx context.Context, uid string, r receipt.Receipt, tz *time.Location) (receipt.SaveOutcome, error) {
	u := s.user(uid)
	rRef := u.Collection("receipts").Doc(r.ID)

	// Nhóm có dòng habit=true mới cần tính lại dự đoán.
	groups := map[string]string{} // group_key -> tên hiển thị
	cats := map[string]string{}
	for _, l := range r.Lines {
		if l.Habit && l.GroupKey != "" {
			groups[l.GroupKey] = l.Name
			cats[l.GroupKey] = l.CategoryKey
		}
	}
	allGroups := make([]string, 0, len(r.Lines))
	seen := map[string]bool{}
	for _, l := range r.Lines {
		if l.GroupKey != "" && !seen[l.GroupKey] {
			seen[l.GroupKey] = true
			allGroups = append(allGroups, l.GroupKey)
		}
	}
	sort.Strings(allGroups)

	var out receipt.SaveOutcome
	err := s.c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out = receipt.SaveOutcome{ReceiptID: r.ID}

		// ---- ĐỌC (mọi lần đọc trước mọi lần ghi) ----
		existing, err := tx.Get(rRef)
		switch {
		case err == nil:
			if h, _ := existing.Data()["payload_hash"].(string); h == r.PayloadHash {
				out.Replayed = true
				return nil
			}
			return apperr.New(apperr.CodeReceiptConflict, "Mã hóa đơn này đã được dùng cho một hóa đơn khác.")
		case !isNotFound(err):
			return err
		}

		userSnap, err := tx.Get(u)
		if err != nil {
			return err
		}
		householdID, _ := userSnap.Data()["household_id"].(string)

		type groupState struct {
			history  []domain.Purchase
			override *domain.Override
		}
		states := map[string]*groupState{}
		for g := range groups {
			st := &groupState{}
			hist, err := tx.Documents(u.Collection("purchases").
				Where("group_key", "==", g).
				Where("habit", "==", true).
				OrderBy("purchased_on", firestore.Desc).
				Limit(purchaseHistory)).GetAll()
			if err != nil {
				return err
			}
			for _, h := range hist {
				if d, err := domain.ParseDate(fmt.Sprint(h.Data()["purchased_on"])); err == nil {
					st.history = append(st.history, domain.Purchase{On: d, Habit: true})
				}
			}
			if snap, err := tx.Get(u.Collection("items").Doc(g)); err == nil {
				st.override = decodeOverride(snap.Data()["override"])
			} else if !isNotFound(err) {
				return err
			}
			states[g] = st
		}

		planRefs := map[string]*firestore.DocumentRef{}
		for _, g := range allGroups {
			ref := u.Collection("plan_items").Doc(g)
			if snap, err := tx.Get(ref); err == nil && snap.Data()["status"] == "open" {
				planRefs[g] = ref
			} else if err != nil && !isNotFound(err) {
				return err
			}
		}

		var askRefs []*firestore.DocumentRef
		if householdID != "" && len(allGroups) > 0 {
			// "in" giới hạn số phần tử; chia lô 30.
			for i := 0; i < len(allGroups); i += 30 {
				end := min(i+30, len(allGroups))
				asks, err := tx.Documents(s.c.Collection("households").Doc(householdID).Collection("asks").
					Where("status", "==", "open").
					Where("group_key", "in", allGroups[i:end])).GetAll()
				if err != nil {
					return err
				}
				for _, a := range asks {
					askRefs = append(askRefs, a.Ref)
				}
			}
		}

		// ---- GHI ----
		now := time.Now()
		lines := make([]map[string]any, 0, len(r.Lines))
		for _, l := range r.Lines {
			lines = append(lines, lineDoc(l))
		}
		if err := tx.Create(rRef, map[string]any{
			"store": r.Store, "purchased_on": r.PurchasedOn.String(), "currency": r.Currency,
			"fx": fxDoc(r.FX), "lines": lines, "total_vnd": int64(r.TotalVND), "discount_vnd": int64(r.DiscountVND),
			"dedupe_key": r.DedupeKey, "payload_hash": r.PayloadHash, "saved_at": now,
		}); err != nil {
			return err
		}
		for _, l := range r.Lines {
			doc := lineDoc(l)
			doc["receipt_id"], doc["purchased_on"], doc["store"] = r.ID, r.PurchasedOn.String(), r.Store
			if err := tx.Set(u.Collection("purchases").Doc(r.ID+"_"+l.ID), doc); err != nil {
				return err
			}
		}
		for g, name := range groups {
			st := states[g]
			hist := append(st.history, domain.Purchase{On: r.PurchasedOn, Habit: true})
			pred := domain.Predict(hist, st.override)
			if err := tx.Set(u.Collection("items").Doc(g), map[string]any{
				"name": name, "name_norm": domain.NormalizeVi(name), "category_key": cats[g],
				"last_purchase": latest(hist).String(), "prediction": predictionDoc(pred),
				"dropped": false, "updated_at": now,
			}, firestore.MergeAll); err != nil {
				return err
			}
		}
		// Tổng tháng dùng Increment để không phải đọc; job cleanup đối chiếu lại hằng đêm
		// bằng cách tính lại từ receipts (dữ liệu suy ra phải tính lại được).
		month := r.PurchasedOn.Month()
		monthDoc := map[string]any{
			"spent_vnd":    firestore.Increment(int64(r.TotalVND)),
			"discount_vnd": firestore.Increment(int64(r.DiscountVND)),
			"receipts":     firestore.Increment(1),
		}
		byCat := map[string]int64{}
		for _, l := range r.Lines {
			if l.VND != nil {
				byCat[catOrOther(l.CategoryKey)] += int64(*l.VND)
			}
		}
		cat := map[string]any{}
		for c, v := range byCat {
			cat[c] = firestore.Increment(v)
		}
		monthDoc["by_category"] = cat
		if err := tx.Set(u.Collection("months").Doc(month), monthDoc, firestore.MergeAll); err != nil {
			return err
		}
		for _, ref := range planRefs {
			if err := tx.Update(ref, []firestore.Update{{Path: "status", Value: "done"}, {Path: "done_by", Value: r.ID}, {Path: "done_at", Value: now}}); err != nil {
				return err
			}
		}
		for _, ref := range askRefs {
			if err := tx.Update(ref, []firestore.Update{{Path: "status", Value: "closed"}, {Path: "closed_by_receipt", Value: r.ID}}); err != nil {
				return err
			}
		}
		return tx.Update(u, []firestore.Update{
			{Path: "receipt_count", Value: firestore.Increment(1)},
			{Path: "last_receipt_at", Value: now},
		})
	}, firestore.MaxAttempts(5))
	if err != nil {
		return receipt.SaveOutcome{}, err
	}
	return out, nil
}

func lineDoc(l receipt.Line) map[string]any {
	var vnd any
	if l.VND != nil {
		vnd = int64(*l.VND)
	}
	return map[string]any{
		"line_id": l.ID, "name": l.Name, "name_norm": l.NameNorm, "qty": l.Qty, "orig": l.Orig,
		"line_total_vnd": vnd, "category_key": l.CategoryKey, "group_key": l.GroupKey, "habit": l.Habit,
	}
}

func catOrOther(c string) string {
	if c == "" {
		return "chua-phan-loai"
	}
	return c
}

func latest(ps []domain.Purchase) domain.Date {
	var d domain.Date
	for _, p := range ps {
		if d.IsZero() || p.On.After(d) {
			d = p.On
		}
	}
	return d
}

func fxDoc(fx *receipt.FX) any {
	if fx == nil {
		return nil
	}
	return map[string]any{"rate": fx.Rate, "source": fx.Source, "at": fx.At}
}

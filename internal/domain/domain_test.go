package domain

import (
	"testing"
	"time"
)

func d(s string) Date {
	dt, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return dt
}

func TestConvertToVND(t *testing.T) {
	tests := []struct {
		name, amount, rate string
		want               VND
		wantErr            bool
	}{
		{"sgd", "12.50", "18450.25", 230628, false}, // 230628.125 -> 230628
		{"round half up", "1", "0.5", 1, false},
		{"integer", "3", "25000", 75000, false},
		{"bad rate", "1", "abc", 0, true},
		{"zero rate", "1", "0", 0, true},
		{"negative", "-1", "100", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertToVND(tt.amount, tt.rate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPredict(t *testing.T) {
	p3 := []Purchase{{d("2026-09-01"), true}, {d("2026-09-08"), true}, {d("2026-09-15"), true}}

	t.Run("one purchase waits", func(t *testing.T) {
		p := Predict(p3[:1], nil)
		if !p.OutOn.IsZero() || p.Confidence != ConfidenceNone {
			t.Fatalf("unexpected %+v", p)
		}
	})
	t.Run("two purchases preliminary", func(t *testing.T) {
		p := Predict(p3[:2], nil)
		if !p.Preliminary || p.Confidence != ConfidenceLow || p.OutOn != d("2026-09-15") {
			t.Fatalf("unexpected %+v", p)
		}
	})
	t.Run("three purchases median", func(t *testing.T) {
		p := Predict(p3, nil)
		if p.IntervalDays != 7 || p.OutOn != d("2026-09-22") || p.Confidence != ConfidenceMid {
			t.Fatalf("unexpected %+v", p)
		}
	})
	t.Run("trip purchases ignored", func(t *testing.T) {
		ps := append([]Purchase{}, p3...)
		ps = append(ps, Purchase{d("2026-09-16"), false})
		if got := Predict(ps, nil); got.OutOn != d("2026-09-22") {
			t.Fatalf("habit=false changed prediction: %+v", got)
		}
	})
	t.Run("only last three intervals", func(t *testing.T) {
		ps := []Purchase{{d("2026-01-01"), true}, {d("2026-03-01"), true}, // khoảng 59 ngày, bị bỏ
			{d("2026-03-08"), true}, {d("2026-03-15"), true}, {d("2026-03-22"), true}}
		p := Predict(ps, nil)
		if p.IntervalDays != 7 || p.Confidence != ConfidenceHigh {
			t.Fatalf("unexpected %+v", p)
		}
	})
	t.Run("override newer than last purchase wins", func(t *testing.T) {
		ov := &Override{Days: 30, SetOn: d("2026-09-20")}
		p := Predict(p3, ov)
		if p.Source != SourceManual || p.OutOn != d("2026-10-20") {
			t.Fatalf("unexpected %+v", p)
		}
	})
	t.Run("newer receipt invalidates override", func(t *testing.T) {
		ov := &Override{Days: 30, SetOn: d("2026-09-10")} // trước lần mua 15/09
		if p := Predict(p3, ov); p.Source != SourcePredicted {
			t.Fatalf("stale override applied: %+v", p)
		}
	})
}

func TestViewAt(t *testing.T) {
	pred := Prediction{OutOn: d("2026-10-20"), PurchaseCount: 4, Source: SourcePredicted}
	tests := []struct {
		today string
		want  Status
	}{
		{"2026-10-21", StatusBuyNow}, // quá ngày dự kiến
		{"2026-10-20", StatusBuyNow},
		{"2026-10-15", StatusSoon},
		{"2026-10-08", StatusUpcoming},
		{"2026-09-30", StatusPlenty},
	}
	for _, tt := range tests {
		if got := ViewAt(pred, d(tt.today), 7).Status; got != tt.want {
			t.Errorf("today %s: got %s, want %s", tt.today, got, tt.want)
		}
	}
	if got := ViewAt(Prediction{OutOn: d("2026-10-20"), PurchaseCount: 2, Source: SourcePredicted}, d("2026-10-10"), 7); got.Status != StatusWaiting {
		t.Errorf("two purchases should not show days left, got %s", got.Status)
	}
	if got := ViewAt(Prediction{ForcedOut: true}, d("2026-10-10"), 7); got.Status != StatusOut {
		t.Errorf("forced out: got %s", got.Status)
	}
}

func TestNormalizeVi(t *testing.T) {
	cases := map[string]string{
		"Dầu gội":             "dau goi",
		"Đường Biên Hòa":      "duong bien hoa",
		"Nước rửa chén 750ml": "nuoc rua chen 750ml",
	}
	for in, want := range cases {
		if got := NormalizeVi(in); got != want {
			t.Errorf("NormalizeVi(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanShopping(t *testing.T) {
	prices := []LastPrice{
		{GroupKey: "sua", Store: "A", Unit: 30000},
		{GroupKey: "sua", Store: "B", Unit: 32000},
		{GroupKey: "trung", Store: "A", Unit: 40000},
		{GroupKey: "trung", Store: "B", Unit: 25000},
	}
	t.Run("small saving keeps one store", func(t *testing.T) {
		p := PlanShopping([]string{"sua", "trung", "ca-phe"}, prices)
		if p.SingleStore == nil || p.SingleStore.Store != "B" || p.SingleStore.Total != 57000 {
			t.Fatalf("single store: %+v", p.SingleStore)
		}
		if p.SplitTotal != 55000 || p.Saving != 2000 || p.Recommend != "single" {
			t.Fatalf("plan: %+v", p)
		}
		if len(p.MissingPrice) != 1 || p.MissingPrice[0] != "ca-phe" {
			t.Fatalf("missing: %v", p.MissingPrice)
		}
	})
	t.Run("no store has everything", func(t *testing.T) {
		p := PlanShopping([]string{"sua", "gao"}, append(prices, LastPrice{GroupKey: "gao", Store: "C", Unit: 90000}))
		if p.SingleStore != nil || p.Recommend != "split" {
			t.Fatalf("plan: %+v", p)
		}
	})
}

func TestQuietHours(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	late := time.Date(2026, 10, 16, 22, 30, 0, 0, loc)
	if !InQuietHours(late, loc) {
		t.Fatal("22:30 should be quiet")
	}
	if got := NextAllowed(late, loc); !got.Equal(time.Date(2026, 10, 17, 7, 0, 0, 0, loc)) {
		t.Fatalf("next allowed = %v", got)
	}
	noon := time.Date(2026, 10, 16, 12, 0, 0, 0, loc)
	if InQuietHours(noon, loc) {
		t.Fatal("noon should not be quiet")
	}
}

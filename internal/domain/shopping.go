package domain

import "sort"

// LastPrice là giá mỗi đơn vị người dùng từng trả cho một nhóm ở một cửa hàng,
// lấy từ hóa đơn đã lưu (không phải giá sale tìm ngoài).
type LastPrice struct {
	GroupKey string
	Store    string
	Unit     VND
	On       Date
}

// SplitThreshold: tiết kiệm từ 10.000đ trở lên mới đề nghị tách nơi mua (ngưỡng khởi điểm, spec mục 5).
const SplitThreshold VND = 10_000

type StopPlan struct {
	Store  string
	Groups []string
	Total  VND
}

type ShoppingPlan struct {
	SingleStore  *StopPlan // nil khi không nơi nào bán đủ món
	Split        []StopPlan
	SplitTotal   VND
	Saving       VND      // SingleStore.Total - SplitTotal, 0 nếu không có SingleStore
	Recommend    string   // "single" | "split"
	MissingPrice []string // chưa có giá để so sánh, không cộng vào tổng
	SingleSource []string // chỉ mua ở một nơi: "chưa đủ để so sánh nơi"
}

// PlanShopping là hàm duy nhất tính "Nơi mua tối ưu"; Giỏ và Giá tốt cùng gọi hàm này
// nên kết luận ở hai màn luôn giống nhau. 1 đơn vị mỗi món.
func PlanShopping(groups []string, prices []LastPrice) ShoppingPlan {
	byGroup := map[string]map[string]VND{}
	for _, p := range prices {
		m, ok := byGroup[p.GroupKey]
		if !ok {
			m = map[string]VND{}
			byGroup[p.GroupKey] = m
		}
		m[p.Store] = p.Unit
	}

	var plan ShoppingPlan
	var priced []string
	for _, g := range groups {
		switch n := len(byGroup[g]); {
		case n == 0:
			plan.MissingPrice = append(plan.MissingPrice, g)
		default:
			if n == 1 {
				plan.SingleSource = append(plan.SingleSource, g)
			}
			priced = append(priced, g)
		}
	}
	if len(priced) == 0 {
		plan.Recommend = "single"
		return plan
	}

	// Tách nơi: mỗi món ở nơi rẻ nhất trong các nơi người dùng từng mua.
	stops := map[string]*StopPlan{}
	for _, g := range priced {
		store, price := cheapest(byGroup[g])
		s, ok := stops[store]
		if !ok {
			s = &StopPlan{Store: store}
			stops[store] = s
		}
		s.Groups = append(s.Groups, g)
		s.Total += price
		plan.SplitTotal += price
	}
	for _, s := range stops {
		plan.Split = append(plan.Split, *s)
	}
	sort.Slice(plan.Split, func(i, j int) bool {
		if len(plan.Split[i].Groups) != len(plan.Split[j].Groups) {
			return len(plan.Split[i].Groups) > len(plan.Split[j].Groups)
		}
		return plan.Split[i].Store < plan.Split[j].Store
	})

	// Mua hết một nơi: chỉ xét nơi có đủ mọi món đã có giá.
	for store := range storesOf(byGroup, priced) {
		total, ok := VND(0), true
		for _, g := range priced {
			p, has := byGroup[g][store]
			if !has {
				ok = false
				break
			}
			total += p
		}
		if ok && (plan.SingleStore == nil || total < plan.SingleStore.Total ||
			(total == plan.SingleStore.Total && store < plan.SingleStore.Store)) {
			plan.SingleStore = &StopPlan{Store: store, Groups: priced, Total: total}
		}
	}

	switch {
	case plan.SingleStore == nil:
		plan.Recommend = "split"
	default:
		plan.Saving = plan.SingleStore.Total - plan.SplitTotal
		if plan.Saving >= SplitThreshold && len(plan.Split) > 1 {
			plan.Recommend = "split"
		} else {
			plan.Recommend = "single"
		}
	}
	return plan
}

func cheapest(m map[string]VND) (string, VND) {
	var bestStore string
	var best VND = -1
	for s, p := range m {
		if best < 0 || p < best || (p == best && s < bestStore) {
			bestStore, best = s, p
		}
	}
	return bestStore, best
}

func storesOf(byGroup map[string]map[string]VND, groups []string) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		for s := range byGroup[g] {
			out[s] = true
		}
	}
	return out
}

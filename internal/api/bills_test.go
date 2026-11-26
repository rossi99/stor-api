package api

import (
	"context"
	"testing"
)

func bill(id, group, kind string, amount float64) map[string]any {
	return map[string]any{
		"id": id, "name": "Bill " + id, "group": group, "kind": kind, "category": "essential",
		"amount": amount, "frequency": "monthly", "months": []int{}, "paidFrom": "joint", "meta": "", "isEstimated": false,
	}
}

func with(b map[string]any, key string, value any) map[string]any {
	out := map[string]any{}
	for k, v := range b {
		out[k] = v
	}
	out[key] = value
	return out
}

func planFor(c client, token, month string) map[string]any {
	return c.expect(200, "GET", "/plan?month="+month, token, nil)
}

func listed(plan map[string]any, key string) []map[string]any {
	var out []map[string]any
	for _, row := range plan[key].([]any) {
		out = append(out, row.(map[string]any))
	}
	return out
}

func billIDs(plan map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, b := range listed(plan, "bills") {
		out[b["id"].(string)] = b
	}
	return out
}

func TestBillsValidation(t *testing.T) {
	c, _ := setup(t)
	ana := signUp(c, "ana@example.com")
	good := bill("rent", "Home", "bill", 900)

	for name, bad := range map[string]map[string]any{
		"kind":           with(good, "kind", "rent"),
		"category":       with(good, "category", "needs"),
		"frequency":      with(good, "frequency", "daily"),
		"months weekly":  with(with(good, "frequency", "weekly"), "months", []int{1}),
		"month 13":       with(good, "months", []int{13}),
		"repeated month": with(good, "months", []int{2, 2}),
		"negative":       with(good, "amount", -1),
		"no name":        with(good, "name", "  "),
		"no group":       with(good, "group", ""),
		"due day":        with(good, "dueDay", 32),
		"id mismatch":    with(good, "id", "other"),
		"partner's":      with(good, "paidFrom", "partner"),
		"no such ledger": with(good, "paidFrom", "third"),
	} {
		if got, out := c.do("PUT", "/bills/rent", ana, bad); got != 400 {
			t.Errorf("%s: %d %v", name, got, out)
		}
	}
	c.expect(400, "GET", "/plan?month=2027-13", ana, nil)
	c.expect(400, "PUT", "/household/budget", ana, map[string]any{"essentialPct": 50, "discretionaryPct": 30, "savingPct": 30, "loanAllocation": 0})
	c.expect(400, "PUT", "/household/budget", ana, map[string]any{"essentialPct": 110, "discretionaryPct": -10, "savingPct": 0, "loanAllocation": 0})
	c.expect(400, "PUT", "/household/budget", ana, map[string]any{"essentialPct": 50, "discretionaryPct": 30, "savingPct": 20, "loanAllocation": -1})
	c.expect(400, "PUT", "/household/budget", ana, map[string]any{"essentialPct": 50, "discretionaryPct": 30, "savingPct": 20, "loanAllocation": 0, "cashCap": -1})
	c.expect(400, "PUT", "/plan/transfers", ana, map[string]any{"month": "", "key": "pot:Home", "done": true})
}

func TestBillsAndPlan(t *testing.T) {
	c, _ := setup(t)
	ana := signUp(c, "ana@example.com")
	sam := signUp(c, "sam@example.com")
	join(c, sam, invite(c, ana))
	c.expect(204, "PUT", "/household/members", ana, []map[string]any{
		{"ledger": "you", "name": "Ana", "netPay": 3000}, {"ledger": "partner", "name": "Sam", "netPay": 2000},
	})
	c.expect(204, "PUT", "/household/budget", ana, map[string]any{
		"essentialPct": 50, "discretionaryPct": 30, "savingPct": 20, "loanAllocation": 250, "cashCap": 300,
	})

	c.expect(204, "PUT", "/bills/rent", ana, with(bill("rent", "Home", "bill", 1200), "dueDay", 28))
	c.expect(204, "PUT", "/bills/rent", ana, with(bill("rent", "Home", "bill", 1250), "dueDay", 28))
	c.expect(204, "PUT", "/bills/water", ana, with(bill("water", "Home", "bill", 120), "frequency", "quarterly"))
	c.expect(204, "PUT", "/bills/loan", sam, bill("loan", "Loans", "loan", 180))
	c.expect(204, "PUT", "/bills/tax", ana, with(bill("tax", "Car", "bill", 20), "months", []int{3, 1}))
	c.expect(204, "PUT", "/bills/isa", ana, with(bill("isa", "ISA", "investment", 100), "category", "saving"))
	c.expect(204, "PUT", "/bills/gym", sam, map[string]any{
		"id": "gym", "name": "Gym", "group": "ignored", "kind": "pot", "category": "essential",
		"amount": 30, "frequency": "monthly", "months": []int{}, "paidFrom": "you", "meta": "Own card", "isEstimated": true,
	})

	// Sam's cash-account bill counts in Sam's totals but is never listed for Ana, nor editable by her.
	c.expect(404, "PUT", "/bills/gym", ana, bill("gym", "Home", "bill", 1))
	c.expect(404, "DELETE", "/bills/gym", ana, nil)
	anaPlan := planFor(c, ana, "2027-01")
	anaBills := billIDs(anaPlan)
	if len(anaBills) != 5 || anaBills["gym"] != nil {
		t.Fatalf("Ana's bills = %v", anaBills)
	}
	if rent := anaBills["rent"]; rent["amount"] != 1250.0 || rent["thisMonth"] != 1250.0 || rent["dueDay"] != 28.0 || rent["paidFrom"] != "joint" {
		t.Fatalf("rent = %v", rent)
	}
	if tax := anaBills["tax"]; tax["thisMonth"] != 20.0 || len(tax["months"].([]any)) != 2 || tax["months"].([]any)[0] != 1.0 {
		t.Fatalf("tax = %v", tax)
	}
	if water := anaBills["water"]; water["thisMonth"] != 40.0 || water["dueDay"] != nil {
		t.Fatalf("water = %v", water)
	}

	samPlan := planFor(c, sam, "2027-01")
	gym := billIDs(samPlan)["gym"]
	if gym["paidFrom"] != "you" || gym["group"] != "" || gym["kind"] != "bill" || gym["category"] != "discretionary" || gym["isEstimated"] != true {
		t.Fatalf("Sam's gym = %v", gym)
	}
	if anaPlan["hub"] != "you" || samPlan["hub"] != "partner" {
		t.Fatalf("hub = %v / %v", anaPlan["hub"], samPlan["hub"])
	}

	// Joint £1,250 + £40 + £180 + £20 + £100; overpay £70; left £3,340 − £600 for cash, so £2,740 is invested.
	for key, want := range map[string]float64{
		"income": 5000, "loanRepayments": 180, "loanOverpay": 70, "fixedInvesting": 100, "billsAndPots": 1560,
		"addInvesting": 2740, "cashEach": 300, "stays": 300,
	} {
		if anaPlan[key] != want {
			t.Errorf("%s = %v, want %v", key, anaPlan[key], want)
		}
	}
	cash := listed(anaPlan, "cashAccounts")
	if cash[0]["ledger"] != "you" || cash[0]["personalBills"] != 0.0 || cash[1]["ledger"] != "partner" || cash[1]["left"] != 270.0 {
		t.Fatalf("cash accounts = %v", cash)
	}
	if s := anaPlan["settings"].(map[string]any); s["cashCap"] != 300.0 || s["loanAllocation"] != 250.0 || s["essentialPct"] != 50.0 {
		t.Fatalf("settings = %v", s)
	}
	forecast := listed(anaPlan, "forecast")
	if len(forecast) != 12 || forecast[0]["month"] != "2027-01" || forecast[2]["month"] != "2027-03" ||
		len(forecast[1]["extraBills"].([]any)) != 0 || forecast[2]["extraBills"].([]any)[0] != "Bill tax" {
		t.Fatalf("forecast = %v", forecast)
	}

	transfers := listed(anaPlan, "transfers")
	if transfers[0]["kind"] != "payIn" || transfers[0]["ledger"] != "partner" || transfers[0]["group"] != nil {
		t.Fatalf("first transfer = %v", transfers[0])
	}
	key := transfers[1]["key"].(string)
	if transfers[1]["group"] != "Home" || transfers[1]["amount"] != 1290.0 || transfers[1]["done"] != false {
		t.Fatalf("second transfer = %v", transfers[1])
	}

	// Ticks are shared by the household and belong to one month.
	c.expect(204, "PUT", "/plan/transfers", sam, map[string]any{"month": "2027-01", "key": key, "done": true})
	c.expect(204, "PUT", "/plan/transfers", sam, map[string]any{"month": "2027-01", "key": key, "done": true})
	if done := listed(planFor(c, ana, "2027-01"), "transfers")[1]["done"]; done != true {
		t.Fatalf("Ana sees done = %v", done)
	}
	if done := listed(planFor(c, ana, "2027-02"), "transfers")[1]["done"]; done != false {
		t.Fatalf("February done = %v", done)
	}
	c.expect(204, "PUT", "/plan/transfers", ana, map[string]any{"month": "2027-01", "key": key, "done": false})
	if done := listed(planFor(c, sam, "2027-01"), "transfers")[1]["done"]; done != false {
		t.Fatalf("after untick done = %v", done)
	}

	c.expect(204, "PUT", "/household/budget", ana, map[string]any{"essentialPct": 40, "discretionaryPct": 40, "savingPct": 20, "loanAllocation": 0, "cashCap": nil})
	if p := planFor(c, ana, "2027-01"); p["settings"].(map[string]any)["cashCap"] != nil || p["addInvesting"] != 0.0 {
		t.Fatalf("uncapped plan = %v", p)
	}

	c.expect(204, "DELETE", "/bills/water", sam, nil)
	c.expect(404, "DELETE", "/bills/water", sam, nil)
	c.expect(204, "DELETE", "/bills/gym", sam, nil)
	if got := billIDs(planFor(c, sam, "")); len(got) != 4 {
		t.Fatalf("after deletes Sam sees %v", got)
	}
}

func TestBillsBlockLeavingAndJoining(t *testing.T) {
	c, pool := setup(t)
	ana := signUp(c, "ana@example.com")
	cara := signUp(c, "cara@example.com")

	c.expect(204, "PUT", "/bills/rent", cara, bill("rent", "Home", "bill", 900))
	c.expect(409, "POST", "/household/join", cara, map[string]string{"code": invite(c, ana)})

	// A personal bill keeps a member's seat, even if it is not their own login's.
	c.expect(204, "PUT", "/household/size", ana, map[string]int{"size": 3})
	_, err := pool.Exec(context.Background(), `
		INSERT INTO bills (household_id, id, name, group_name, kind, category, amount_pence, frequency, paid_from)
		SELECT household_id, 'p3', 'Phone', '', 'bill', 'discretionary', 1000, 'monthly', 3 FROM members m
		JOIN users u ON u.id = m.user_id WHERE u.email = 'ana@example.com'`)
	if err != nil {
		t.Fatal(err)
	}
	c.expect(409, "PUT", "/household/size", ana, map[string]int{"size": 2})
}

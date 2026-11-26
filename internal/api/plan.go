package api

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"
)

// The budget maths from the household's bills sheet. Money is in pence throughout.

// hubSlot is the household's creator: their account receives everyone's pay and sends the payday transfers.
const hubSlot = 1

var (
	billKinds       = []string{"bill", "pot", "loan", "saving", "investment"}
	billCategories  = []string{"essential", "discretionary", "saving"}
	billFrequencies = []string{"monthly", "weekly", "quarterly", "yearly"}
)

type planBill struct {
	ID, Name, Group, Kind, Category, Frequency string
	Amount                                     int64
	Months                                     []int16 // empty means every month
	PaidFrom                                   int16   // 0 is the joint pots, else the member slot
}

type budgetSettings struct {
	EssentialPct, DiscretionaryPct, SavingPct int64
	LoanAllocation                            int64
	CashCap                                   *int64 // nil means uncapped
}

// thisMonth is how much of a bill lands in month m. Months-only bills land in full in their months;
// the rest spread by frequency, with a week counted as a quarter of a month as the sheet does.
func thisMonth(b planBill, m time.Month) int64 {
	if len(b.Months) > 0 {
		if slices.Contains(b.Months, int16(m)) {
			return b.Amount
		}
		return 0
	}
	switch b.Frequency {
	case "weekly":
		return b.Amount * 4
	case "quarterly":
		return roundDiv(b.Amount, 3)
	case "yearly":
		return roundDiv(b.Amount, 12)
	}
	return b.Amount
}

// roundDiv rounds half up; amounts here are never negative.
func roundDiv(a, n int64) int64 { return (a + n/2) / n }

type monthPlan struct {
	Income, LoanRepayments, LoanOverpay, FixedInvesting, BillsAndPots, AddInvesting, CashEach int64
	ThisMonth                                                                                 map[string]int64 // by bill id
}

// budgetMonth works out one month. pays holds each member's take-home pay in slot order.
func budgetMonth(pays []int64, s budgetSettings, bills []planBill, m time.Month) monthPlan {
	p := monthPlan{ThisMonth: make(map[string]int64, len(bills))}
	for _, pay := range pays {
		p.Income += pay
	}
	var joint int64
	for _, b := range bills {
		amount := thisMonth(b, m)
		p.ThisMonth[b.ID] = amount
		if b.PaidFrom != 0 {
			continue
		}
		joint += amount
		switch b.Kind {
		case "loan":
			p.LoanRepayments += amount
		case "investment":
			p.FixedInvesting += amount
		}
	}
	p.LoanOverpay = max(0, s.LoanAllocation-p.LoanRepayments)
	p.BillsAndPots = joint - p.FixedInvesting + p.LoanOverpay
	left, people := p.Income-p.BillsAndPots-p.FixedInvesting, int64(len(pays))
	if s.CashCap == nil {
		p.CashEach = left / people
		return p
	}
	limit := *s.CashCap
	p.AddInvesting = max(0, left-people*limit)
	// Odd pennies from the split stay in the hub's account.
	p.CashEach = min(limit, (left-p.AddInvesting)/people)
	return p
}

type planRow struct {
	Name   string
	Amount int64
}

type planTransfer struct {
	Key, Kind, Group string
	Slot             int16 // who pays in, or whose cash account receives; 0 otherwise
	Amount           int64
}

type planSplit struct {
	Category                 string
	Allocated, Actual, Using int64
	Status                   string
}

type planCash struct {
	Slot                           int16
	Allowance, PersonalBills, Left int64
}

type planForecast struct {
	Month                                         time.Time
	Bills, PotsAndSavings, AddInvesting, CashEach int64
	ExtraBills                                    []string
	IsShort                                       bool
}

type plan struct {
	monthPlan
	Groups    []planRow // where the money goes
	Transfers []planTransfer
	Stays     int64 // what the hub keeps after sending every transfer
	Split     []planSplit
	Biggest   []planBill
	Cash      []planCash
	Forecast  []planForecast
}

// buildPlan is the budget for month (the first of a month) plus a 12-month forecast from it.
func buildPlan(pays []int64, s budgetSettings, bills []planBill, month time.Time) plan {
	p := plan{monthPlan: budgetMonth(pays, s, bills, month.Month())}
	amount := func(b planBill) int64 { return p.ThisMonth[b.ID] }

	pots, loans, investing, where := map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}
	savingsOnly := map[string]bool{}
	using := map[string]int64{"saving": p.LoanOverpay}
	personal := map[int16]int64{}
	for _, b := range bills {
		a := amount(b)
		if b.PaidFrom != 0 {
			personal[b.PaidFrom] += a
			continue
		}
		using[b.Category] += a
		switch b.Kind {
		case "investment":
			investing[b.Group] += a
			where["Investing"] += a
			continue
		case "saving":
			where["Savings pots"] += a
		default:
			where[b.Group] += a
			if b.Kind == "loan" {
				loans[b.Group] += a
			}
		}
		// Groups holding only savings pots are paid after the spending ones.
		if _, seen := savingsOnly[b.Group]; !seen {
			savingsOnly[b.Group] = true
		}
		savingsOnly[b.Group] = savingsOnly[b.Group] && b.Kind == "saving"
		pots[b.Group] += a
	}

	loanGroup := largest(loans, "Loans & debts")
	pots[loanGroup] += p.LoanOverpay
	where[loanGroup] += p.LoanOverpay
	investing[largest(investing, "Investing")] += p.AddInvesting
	where["Investing"] += p.AddInvesting
	where["Cash accounts"] = int64(len(pays)) * max(0, p.CashEach)
	for _, row := range rows(where) {
		if row.Amount > 0 {
			p.Groups = append(p.Groups, row)
		}
	}

	for i, pay := range pays {
		if slot := int16(i + 1); slot != hubSlot && pay > 0 {
			p.Transfers = append(p.Transfers, planTransfer{Key: fmt.Sprintf("pay:%d", slot), Kind: "payIn", Slot: slot, Amount: pay})
		}
	}
	var sent int64
	send := func(t planTransfer) {
		if t.Amount > 0 {
			p.Transfers = append(p.Transfers, t)
			sent += t.Amount
		}
	}
	for _, savings := range []bool{false, true} {
		for _, row := range rows(pots) {
			if savingsOnly[row.Name] == savings {
				send(planTransfer{Key: "pot:" + row.Name, Kind: "pot", Group: row.Name, Amount: row.Amount})
			}
		}
	}
	for _, row := range rows(investing) {
		send(planTransfer{Key: "investing:" + row.Name, Kind: "investing", Group: row.Name, Amount: row.Amount})
	}
	for i := range pays {
		if slot := int16(i + 1); slot != hubSlot {
			send(planTransfer{Key: fmt.Sprintf("cash:%d", slot), Kind: "cash", Slot: slot, Amount: p.CashEach})
		}
	}
	p.Stays = p.Income - sent

	// 50/30/20: discretionary absorbs any essential overspend, and saving gets discretionary's spare.
	allocate := func(pct int64) int64 { return roundDiv(p.Income*pct, 100) }
	essential, discretionary, saving := allocate(s.EssentialPct), allocate(s.DiscretionaryPct), allocate(s.SavingPct)
	discretionaryActual := discretionary - max(0, using["essential"]-essential)
	p.Split = []planSplit{
		split("essential", essential, essential, using["essential"]),
		split("discretionary", discretionary, discretionaryActual, using["discretionary"]),
		split("saving", saving, saving+discretionaryActual-using["discretionary"], using["saving"]),
	}

	for _, b := range bills {
		if b.PaidFrom == 0 && b.Kind != "saving" && b.Kind != "investment" && amount(b) > 0 {
			p.Biggest = append(p.Biggest, b)
		}
	}
	slices.SortStableFunc(p.Biggest, func(x, y planBill) int {
		return cmp.Or(cmp.Compare(amount(y), amount(x)), cmp.Compare(x.Name, y.Name))
	})
	p.Biggest = p.Biggest[:min(8, len(p.Biggest))]

	for i := range pays {
		slot := int16(i + 1)
		p.Cash = append(p.Cash, planCash{Slot: slot, Allowance: p.CashEach, PersonalBills: personal[slot], Left: p.CashEach - personal[slot]})
	}

	for i := range 12 {
		m := month.AddDate(0, i, 0)
		f := budgetMonth(pays, s, bills, m.Month())
		row := planForecast{Month: m, Bills: f.LoanOverpay, AddInvesting: f.AddInvesting, CashEach: f.CashEach,
			IsShort: s.CashCap != nil && f.CashEach < *s.CashCap}
		for _, b := range bills {
			a := f.ThisMonth[b.ID]
			if b.PaidFrom != 0 {
				continue
			}
			switch b.Kind {
			case "bill", "loan":
				row.Bills += a
			case "pot", "saving":
				row.PotsAndSavings += a
			}
			if len(b.Months) > 0 && a > 0 {
				row.ExtraBills = append(row.ExtraBills, b.Name)
			}
		}
		p.Forecast = append(p.Forecast, row)
	}
	return p
}

// split rates a 50/30/20 line the way the sheet does: over, nearly used (95%), or comfortably spare.
func split(category string, allocated, actual, using int64) planSplit {
	status := "spare"
	switch {
	case using > actual:
		status = "over"
	case using*100 >= actual*95:
		status = "left"
	}
	return planSplit{Category: category, Allocated: allocated, Actual: actual, Using: using, Status: status}
}

// rows lists a total per name, largest first and then by name, so output is stable.
func rows(totals map[string]int64) []planRow {
	out := make([]planRow, 0, len(totals))
	for _, name := range slices.Sorted(maps.Keys(totals)) {
		out = append(out, planRow{Name: name, Amount: totals[name]})
	}
	slices.SortStableFunc(out, func(x, y planRow) int { return cmp.Compare(y.Amount, x.Amount) })
	return out
}

// largest names the biggest total, or fallback when there are none.
func largest(totals map[string]int64, fallback string) string {
	if r := rows(totals); len(r) > 0 {
		return r[0].Name
	}
	return fallback
}

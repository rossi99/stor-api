package api

import (
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"rossi99/stor/internal/store"
)

type billJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Group       string  `json:"group"`
	Kind        string  `json:"kind"`
	Category    string  `json:"category"`
	Amount      float64 `json:"amount"`
	Frequency   string  `json:"frequency"`
	Months      []int   `json:"months"`
	PaidFrom    string  `json:"paidFrom"`
	DueDay      *int    `json:"dueDay"`
	Meta        string  `json:"meta"`
	IsEstimated bool    `json:"isEstimated"`
	ThisMonth   float64 `json:"thisMonth"` // worked out by the server; ignored on write
}

type budgetJSON struct {
	EssentialPct     int64    `json:"essentialPct"`
	DiscretionaryPct int64    `json:"discretionaryPct"`
	SavingPct        int64    `json:"savingPct"`
	LoanAllocation   float64  `json:"loanAllocation"`
	CashCap          *float64 `json:"cashCap"`
}

// putBill creates or replaces a bill. Personal bills can only be your own.
func (s *Server) putBill(w http.ResponseWriter, r *http.Request, v viewer) {
	var b billJSON
	if !decode(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	paidFrom, ledgerOK := v.slotFor(b.PaidFrom)
	amount, amountOK := pence(b.Amount)
	name, group := strings.TrimSpace(b.Name), strings.TrimSpace(b.Group)
	if paidFrom != 0 {
		// A cash-account bill sits outside the pots and the 50/30/20 split.
		group, b.Kind, b.Category = "", "bill", "discretionary"
	}
	months, monthsOK := billMonths(b.Months, b.Frequency)
	if len(id) > 64 || (b.ID != "" && b.ID != id) || !ledgerOK || (paidFrom != 0 && paidFrom != v.slot) ||
		name == "" || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(b.Meta) > 100 ||
		(paidFrom == 0 && (group == "" || utf8.RuneCountInString(group) > 60)) ||
		!slices.Contains(billKinds, b.Kind) || !slices.Contains(billCategories, b.Category) ||
		!slices.Contains(billFrequencies, b.Frequency) || !monthsOK || !amountOK || amount < 0 ||
		(b.DueDay != nil && (*b.DueDay < 1 || *b.DueDay > 31)) {
		fail(w, http.StatusBadRequest, "invalid bill")
		return
	}
	params := store.UpsertBillParams{
		HouseholdID: v.household, ID: id, Name: name, GroupName: group, Kind: b.Kind, Category: b.Category,
		AmountPence: amount, Frequency: b.Frequency, Months: months, PaidFrom: paidFrom,
		Meta: strings.TrimSpace(b.Meta), IsEstimated: b.IsEstimated, ViewerSlot: v.slot,
	}
	if b.DueDay != nil {
		params.DueDay = pgtype.Int2{Int16: int16(*b.DueDay), Valid: true}
	}
	n, err := s.q.UpsertBill(r.Context(), params)
	if err != nil {
		internal(w, r, err)
		return
	}
	if n == 0 {
		fail(w, http.StatusNotFound, "no such bill")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// billMonths accepts distinct months 1 to 12, and only for a monthly bill.
func billMonths(months []int, frequency string) ([]int16, bool) {
	out := make([]int16, 0, len(months))
	for _, m := range months {
		if m < 1 || m > 12 || slices.Contains(out, int16(m)) {
			return nil, false
		}
		out = append(out, int16(m))
	}
	slices.Sort(out)
	return out, len(out) == 0 || frequency == "monthly"
}

func (s *Server) deleteBill(w http.ResponseWriter, r *http.Request, v viewer) {
	n, err := s.q.DeleteBill(r.Context(), store.DeleteBillParams{HouseholdID: v.household, ID: r.PathValue("id"), ViewerSlot: v.slot})
	if err != nil {
		internal(w, r, err)
		return
	}
	if n == 0 {
		fail(w, http.StatusNotFound, "no such bill")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setBudget(w http.ResponseWriter, r *http.Request, v viewer) {
	var b budgetJSON
	if !decode(w, r, &b) {
		return
	}
	loan, loanOK := pence(b.LoanAllocation)
	params := store.SetBudgetSettingsParams{
		ID: v.household, EssentialPct: int32(b.EssentialPct), DiscretionaryPct: int32(b.DiscretionaryPct),
		SavingPct: int32(b.SavingPct), LoanAllocationPence: loan,
	}
	capOK := true
	if b.CashCap != nil {
		limit, ok := pence(*b.CashCap)
		capOK = ok && limit >= 0
		params.CashCapPence = pgtype.Int8{Int64: limit, Valid: true}
	}
	if !loanOK || loan < 0 || !capOK || min(b.EssentialPct, b.DiscretionaryPct, b.SavingPct) < 0 ||
		b.EssentialPct+b.DiscretionaryPct+b.SavingPct != 100 {
		fail(w, http.StatusBadRequest, "invalid budget")
		return
	}
	if err := s.q.SetBudgetSettings(r.Context(), params); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// planMonth reads "YYYY-MM" as the first of that month; empty means the current month in UTC.
func planMonth(s string) (time.Time, bool) {
	if s == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	}
	month, err := time.Parse("2006-01", s)
	return month, err == nil
}

func (s *Server) setTransfer(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct {
		Month string `json:"month"`
		Key   string `json:"key"`
		Done  bool   `json:"done"`
	}
	if !decode(w, r, &body) {
		return
	}
	month, ok := planMonth(body.Month)
	if body.Month == "" || !ok || body.Key == "" || len(body.Key) > 200 {
		fail(w, http.StatusBadRequest, "invalid transfer")
		return
	}
	date := pgtype.Date{Time: month, Valid: true}
	var err error
	if body.Done {
		err = s.q.TickTransfer(r.Context(), store.TickTransferParams{HouseholdID: v.household, Month: date, Key: body.Key})
	} else {
		err = s.q.UntickTransfer(r.Context(), store.UntickTransferParams{HouseholdID: v.household, Month: date, Key: body.Key})
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type planJSON struct {
	Month          string         `json:"month"`
	Hub            string         `json:"hub"`
	Settings       budgetJSON     `json:"settings"`
	Income         float64        `json:"income"`
	LoanRepayments float64        `json:"loanRepayments"`
	LoanOverpay    float64        `json:"loanOverpay"`
	FixedInvesting float64        `json:"fixedInvesting"`
	AddInvesting   float64        `json:"addInvesting"`
	BillsAndPots   float64        `json:"billsAndPots"`
	CashEach       float64        `json:"cashEach"`
	Stays          float64        `json:"stays"`
	Bills          []billJSON     `json:"bills"`
	Groups         []rowJSON      `json:"groups"`
	Transfers      []transferJSON `json:"transfers"`
	Split          []splitJSON    `json:"split"`
	Biggest        []biggestJSON  `json:"biggest"`
	CashAccounts   []cashJSON     `json:"cashAccounts"`
	Forecast       []forecastJSON `json:"forecast"`
}

type rowJSON struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
}

type transferJSON struct {
	Key    string  `json:"key"`
	Kind   string  `json:"kind"`
	Ledger *string `json:"ledger,omitempty"`
	Group  *string `json:"group,omitempty"`
	Amount float64 `json:"amount"`
	Done   bool    `json:"done"`
}

type splitJSON struct {
	Category  string  `json:"category"`
	Allocated float64 `json:"allocated"`
	Actual    float64 `json:"actual"`
	Using     float64 `json:"using"`
	Status    string  `json:"status"`
}

type biggestJSON struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Group  string  `json:"group"`
	Amount float64 `json:"amount"`
}

type cashJSON struct {
	Ledger        string  `json:"ledger"`
	Allowance     float64 `json:"allowance"`
	PersonalBills float64 `json:"personalBills"`
	Left          float64 `json:"left"`
}

type forecastJSON struct {
	Month          string   `json:"month"`
	Bills          float64  `json:"bills"`
	PotsAndSavings float64  `json:"potsAndSavings"`
	AddInvesting   float64  `json:"addInvesting"`
	CashEach       float64  `json:"cashEach"`
	ExtraBills     []string `json:"extraBills"`
	IsShort        bool     `json:"isShort"`
}

// getPlan is the budget month with every member's bills counted, but only joint ones and
// the viewer's own listed: other members' cash-account bills show only as their totals.
func (s *Server) getPlan(w http.ResponseWriter, r *http.Request, v viewer) {
	month, ok := planMonth(r.URL.Query().Get("month"))
	if !ok {
		fail(w, http.StatusBadRequest, "month must be YYYY-MM")
		return
	}
	ctx := r.Context()
	members, err := s.q.ListMembers(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	settingsRow, err := s.q.GetBudgetSettings(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	billRows, err := s.q.ListBills(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	ticked, err := s.q.ListTicks(ctx, store.ListTicksParams{HouseholdID: v.household, Month: pgtype.Date{Time: month, Valid: true}})
	if err != nil {
		internal(w, r, err)
		return
	}

	pays := make([]int64, len(members))
	for i, m := range members {
		pays[i] = m.NetPayPence
	}
	settings := budgetSettings{
		EssentialPct: int64(settingsRow.EssentialPct), DiscretionaryPct: int64(settingsRow.DiscretionaryPct),
		SavingPct: int64(settingsRow.SavingPct), LoanAllocation: settingsRow.LoanAllocationPence,
	}
	out := planJSON{
		Month: month.Format("2006-01"), Hub: v.ledgerFor(hubSlot),
		Settings: budgetJSON{
			EssentialPct: settings.EssentialPct, DiscretionaryPct: settings.DiscretionaryPct, SavingPct: settings.SavingPct,
			LoanAllocation: pounds(settings.LoanAllocation),
		},
		Bills: []billJSON{}, Groups: []rowJSON{}, Transfers: []transferJSON{}, Biggest: []biggestJSON{},
	}
	if settingsRow.CashCapPence.Valid {
		settings.CashCap = &settingsRow.CashCapPence.Int64
		limit := pounds(settingsRow.CashCapPence.Int64)
		out.Settings.CashCap = &limit
	}
	bills := make([]planBill, len(billRows))
	for i, b := range billRows {
		bills[i] = planBill{
			ID: b.ID, Name: b.Name, Group: b.GroupName, Kind: b.Kind, Category: b.Category, Frequency: b.Frequency,
			Amount: b.AmountPence, Months: b.Months, PaidFrom: b.PaidFrom,
		}
	}
	p := buildPlan(pays, settings, bills, month)

	out.Income, out.LoanRepayments, out.LoanOverpay = pounds(p.Income), pounds(p.LoanRepayments), pounds(p.LoanOverpay)
	out.FixedInvesting, out.AddInvesting, out.BillsAndPots = pounds(p.FixedInvesting), pounds(p.AddInvesting), pounds(p.BillsAndPots)
	out.CashEach, out.Stays = pounds(p.CashEach), pounds(p.Stays)
	for _, b := range billRows {
		if b.PaidFrom == 0 || b.PaidFrom == v.slot {
			out.Bills = append(out.Bills, billOut(b, p.ThisMonth[b.ID], v))
		}
	}
	for _, g := range p.Groups {
		out.Groups = append(out.Groups, rowJSON{Name: g.Name, Amount: pounds(g.Amount)})
	}
	for _, t := range p.Transfers {
		row := transferJSON{Key: t.Key, Kind: t.Kind, Amount: pounds(t.Amount), Done: slices.Contains(ticked, t.Key)}
		if t.Slot != 0 {
			ledger := v.ledgerFor(t.Slot)
			row.Ledger = &ledger
		}
		if t.Group != "" {
			group := t.Group
			row.Group = &group
		}
		out.Transfers = append(out.Transfers, row)
	}
	for _, sp := range p.Split {
		out.Split = append(out.Split, splitJSON{
			Category: sp.Category, Allocated: pounds(sp.Allocated), Actual: pounds(sp.Actual), Using: pounds(sp.Using), Status: sp.Status,
		})
	}
	for _, b := range p.Biggest {
		out.Biggest = append(out.Biggest, biggestJSON{ID: b.ID, Name: b.Name, Group: b.Group, Amount: pounds(p.ThisMonth[b.ID])})
	}
	// The viewer first, then everyone else in slot order, as in the household snapshot.
	for _, c := range p.Cash {
		row := cashJSON{Ledger: v.ledgerFor(c.Slot), Allowance: pounds(c.Allowance), PersonalBills: pounds(c.PersonalBills), Left: pounds(c.Left)}
		if c.Slot == v.slot {
			out.CashAccounts = append([]cashJSON{row}, out.CashAccounts...)
		} else {
			out.CashAccounts = append(out.CashAccounts, row)
		}
	}
	for _, f := range p.Forecast {
		out.Forecast = append(out.Forecast, forecastJSON{
			Month: f.Month.Format("2006-01"), Bills: pounds(f.Bills), PotsAndSavings: pounds(f.PotsAndSavings),
			AddInvesting: pounds(f.AddInvesting), CashEach: pounds(f.CashEach),
			ExtraBills: append([]string{}, f.ExtraBills...), IsShort: f.IsShort,
		})
	}
	reply(w, http.StatusOK, out)
}

func billOut(b store.ListBillsRow, thisMonth int64, v viewer) billJSON {
	out := billJSON{
		ID: b.ID, Name: b.Name, Group: b.GroupName, Kind: b.Kind, Category: b.Category, Amount: pounds(b.AmountPence),
		Frequency: b.Frequency, Months: []int{}, PaidFrom: v.ledgerFor(b.PaidFrom), Meta: b.Meta,
		IsEstimated: b.IsEstimated, ThisMonth: pounds(thisMonth),
	}
	for _, m := range b.Months {
		out.Months = append(out.Months, int(m))
	}
	if b.DueDay.Valid {
		day := int(b.DueDay.Int16)
		out.DueDay = &day
	}
	return out
}

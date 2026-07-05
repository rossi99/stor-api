package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"stor.app/api/internal/domain"
	"stor.app/api/internal/finance"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

func (h *Handler) ListIncomes(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	rows, err := h.Store.ListIncomesByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list incomes", "error", err)
		httpx.Internal(w)
		return
	}
	out := make([]incomeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, incomeResponse{
			UserID: row.UserID, MemberName: row.MemberName,
			GrossMonthlyMinor: row.GrossMonthlyMinor, NetMonthlyMinor: row.NetMonthlyMinor,
			Currency: "GBP",
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"incomes": out})
}

func (h *Handler) ListPensions(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	rows, err := h.Store.ListPensionsByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list pensions", "error", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, map[string]any{
			"userId": p.UserID, "memberName": p.MemberName,
			"currentValueMinor": p.CurrentValueMinor, "targetMinor": p.TargetMinor,
			"lastUpdated": p.UpdatedAt, "currency": "GBP",
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"pensions": out})
}

func (h *Handler) ListISAs(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	rows, err := h.Store.ListISAsByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list isas", "error", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, i := range rows {
		out = append(out, map[string]any{
			"userId": i.UserID, "memberName": i.MemberName, "type": i.Type,
			"balanceMinor":               i.BalanceMinor,
			"contributionsThisYearMinor": i.ContributionsThisYearMinor,
			"annualAllowanceMinor":       i.AnnualAllowanceMinor, "currency": "GBP",
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"isas": out})
}

// summaryInputs loads everything the finance package needs for a household.
func (h *Handler) summaryInputs(r *http.Request, m reqctx.Membership) ([]finance.MemberIncome, []finance.ExpenseLine, []store.SavingsPot, error) {
	incomes, err := h.Store.ListIncomesByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		return nil, nil, nil, err
	}
	approved := domain.ExpenseStatusApproved
	expenses, err := h.Store.ListExpenses(r.Context(), store.ListExpensesParams{
		HouseholdID: m.HouseholdID, Status: &approved,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	pots, err := h.Store.ListSavingsPots(r.Context(), m.HouseholdID)
	if err != nil {
		return nil, nil, nil, err
	}

	mi := make([]finance.MemberIncome, 0, len(incomes))
	for _, inc := range incomes {
		mi = append(mi, finance.MemberIncome{UserID: inc.UserID, NetMonthlyMinor: inc.NetMonthlyMinor})
	}
	lines := make([]finance.ExpenseLine, 0, len(expenses))
	for _, e := range expenses {
		lines = append(lines, finance.ExpenseLine{
			AmountMinor: e.AmountMinor,
			Frequency:   domain.Frequency(e.Frequency),
			Category:    domain.Category(e.Category),
		})
	}
	return mi, lines, pots, nil
}

func potContributions(pots []store.SavingsPot) int64 {
	var total int64
	for _, p := range pots {
		total += p.MonthlyContributionMinor
	}
	return total
}

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	incomes, expenses, pots, err := h.summaryInputs(r, m)
	if err != nil {
		h.Log.Error("summary inputs", "error", err)
		httpx.Internal(w)
		return
	}
	s := finance.Summarise(incomes, expenses, potContributions(pots), m.SplitMethod)

	// Maintain the month-on-month comparison: record this month's total, read
	// last month's if we have one.
	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	prevMonth := thisMonth.AddDate(0, -1, 0)
	if err := h.Store.UpsertMonthlySnapshot(r.Context(), store.UpsertMonthlySnapshotParams{
		HouseholdID: m.HouseholdID, Month: pgDate(thisMonth), TotalExpensesMinor: s.MonthlyExpensesMinor,
	}); err != nil {
		h.Log.Error("upsert snapshot", "error", err)
	}
	var previousMonthExpensesMinor *int64
	if snap, err := h.Store.GetMonthlySnapshot(r.Context(), store.GetMonthlySnapshotParams{
		HouseholdID: m.HouseholdID, Month: pgDate(prevMonth),
	}); err == nil {
		previousMonthExpensesMinor = &snap.TotalExpensesMinor
	}

	memberSurplus := make([]map[string]any, 0, len(s.MemberSurplusMinor))
	for _, inc := range incomes {
		memberSurplus = append(memberSurplus, map[string]any{
			"userId": inc.UserID, "amountMinor": s.MemberSurplusMinor[inc.UserID],
		})
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"currency":             "GBP",
		"monthlyIncomeMinor":   s.MonthlyIncomeMinor,
		"monthlyExpensesMinor": s.MonthlyExpensesMinor,
		"monthlySavingsMinor":  s.MonthlySavingsMinor,
		"netSurplusMinor":      s.NetSurplusMinor,
		"framework": frameworkBody{
			NeedsPercent: m.NeedsPercent, WantsPercent: m.WantsPercent, SavingsPercent: m.SavingsPercent,
		},
		"buckets": map[string]any{
			"needs":   map[string]int64{"actualMinor": s.Buckets[domain.BucketNeeds]},
			"wants":   map[string]int64{"actualMinor": s.Buckets[domain.BucketWants]},
			"savings": map[string]int64{"actualMinor": s.Buckets[domain.BucketSavings]},
		},
		"memberSurplus":              memberSurplus,
		"previousMonthExpensesMinor": previousMonthExpensesMinor,
		"surplusSplitMethod":         string(m.SplitMethod),
	})
}

// Forecast answers the what-if scenario from ForecastView: an extra monthly
// expense and/or an adjusted contribution to one pot, projected forward.
func (h *Handler) Forecast(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())

	q := r.URL.Query()
	additional, err := queryInt64(q.Get("additionalMonthlyExpenseMinor"), 0)
	if err != nil || additional < 0 || additional > domain.MaxAmountMinor {
		httpx.BadRequest(w, "additionalMonthlyExpenseMinor must be a non-negative amount")
		return
	}
	adjustment, err := queryInt64(q.Get("savingsAdjustmentMinor"), 0)
	if err != nil || adjustment > domain.MaxAmountMinor || adjustment < -domain.MaxAmountMinor {
		httpx.BadRequest(w, "savingsAdjustmentMinor out of range")
		return
	}
	var affectedPot *uuid.UUID
	if s := q.Get("potId"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.BadRequest(w, "invalid potId")
			return
		}
		affectedPot = &id
	}

	incomes, expenses, pots, err := h.summaryInputs(r, m)
	if err != nil {
		h.Log.Error("forecast inputs", "error", err)
		httpx.Internal(w)
		return
	}

	baseline := finance.Summarise(incomes, expenses, potContributions(pots), m.SplitMethod)
	adjustedSurplus := baseline.NetSurplusMinor - additional - adjustment

	now := time.Now().UTC()
	const horizonMonths = 12
	potOut := make([]map[string]any, 0, len(pots))
	for _, p := range pots {
		contribution := p.MonthlyContributionMinor
		if affectedPot != nil && p.ID == *affectedPot {
			contribution += adjustment
			if contribution < 0 {
				contribution = 0
			}
		}
		potOut = append(potOut, map[string]any{
			"id":                       p.ID,
			"name":                     p.Name,
			"emoji":                    p.Emoji,
			"currentMinor":             p.CurrentMinor,
			"targetMinor":              p.TargetMinor,
			"monthlyContributionMinor": contribution,
			"timeToGoal":               finance.GoalTime(now, p.CurrentMinor, p.TargetMinor, contribution),
			"projectedBalancesMinor":   finance.ProjectedBalances(p.CurrentMinor, contribution, horizonMonths, p.TargetMinor),
		})
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"currency":                      "GBP",
		"baselineNetSurplusMinor":       baseline.NetSurplusMinor,
		"adjustedNetSurplusMinor":       adjustedSurplus,
		"additionalMonthlyExpenseMinor": additional,
		"savingsAdjustmentMinor":        adjustment,
		"horizonMonths":                 horizonMonths,
		"pots":                          potOut,
	})
}

func queryInt64(s string, fallback int64) (int64, error) {
	if s == "" {
		return fallback, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

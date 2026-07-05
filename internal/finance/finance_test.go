package finance

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/domain"
)

var (
	memberA = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	memberB = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

func TestSummariseProportionalSplit(t *testing.T) {
	// Mirrors the app's mock household shape: two earners, income split ~60/40.
	incomes := []MemberIncome{
		{UserID: memberA, NetMonthlyMinor: 300_000}, // £3,000
		{UserID: memberB, NetMonthlyMinor: 200_000}, // £2,000
	}
	expenses := []ExpenseLine{
		{AmountMinor: 120_000, Frequency: domain.FrequencyMonthly, Category: domain.CategoryBills},      // needs
		{AmountMinor: 10_000, Frequency: domain.FrequencyWeekly, Category: domain.CategoryGroceries},    // needs £433.33
		{AmountMinor: 120_000, Frequency: domain.FrequencyAnnual, Category: domain.CategoryOther},       // wants £100
		{AmountMinor: 5_000, Frequency: domain.FrequencyMonthly, Category: domain.CategorySinkingFunds}, // savings
	}
	s := Summarise(incomes, expenses, 50_000, domain.SplitProportional)

	if s.MonthlyIncomeMinor != 500_000 {
		t.Errorf("income = %d, want 500000", s.MonthlyIncomeMinor)
	}
	wantExpenses := int64(120_000 + 43_333 + 10_000 + 5_000)
	if s.MonthlyExpensesMinor != wantExpenses {
		t.Errorf("expenses = %d, want %d", s.MonthlyExpensesMinor, wantExpenses)
	}
	if s.Buckets[domain.BucketNeeds] != 163_333 {
		t.Errorf("needs = %d, want 163333", s.Buckets[domain.BucketNeeds])
	}
	if s.Buckets[domain.BucketWants] != 10_000 {
		t.Errorf("wants = %d, want 10000", s.Buckets[domain.BucketWants])
	}
	if s.Buckets[domain.BucketSavings] != 5_000 {
		t.Errorf("savings bucket = %d, want 5000", s.Buckets[domain.BucketSavings])
	}
	wantSurplus := int64(500_000) - wantExpenses - 50_000
	if s.NetSurplusMinor != wantSurplus {
		t.Errorf("surplus = %d, want %d", s.NetSurplusMinor, wantSurplus)
	}
	// Proportional: A gets 3/5, B absorbs the remainder; shares must sum exactly.
	if s.MemberSurplusMinor[memberA] != wantSurplus*3/5 {
		t.Errorf("member A share = %d, want %d", s.MemberSurplusMinor[memberA], wantSurplus*3/5)
	}
	if s.MemberSurplusMinor[memberA]+s.MemberSurplusMinor[memberB] != wantSurplus {
		t.Errorf("shares don't sum to surplus")
	}
}

func TestSummariseEvenSplitSumsExactly(t *testing.T) {
	incomes := []MemberIncome{
		{UserID: memberA, NetMonthlyMinor: 100_000},
		{UserID: memberB, NetMonthlyMinor: 100_000},
	}
	// Odd surplus (£999.99) cannot split evenly; remainder must not be lost.
	s := Summarise(incomes, []ExpenseLine{{AmountMinor: 100_001, Frequency: domain.FrequencyMonthly, Category: domain.CategoryBills}}, 0, domain.SplitEven)
	if s.NetSurplusMinor != 99_999 {
		t.Fatalf("surplus = %d, want 99999", s.NetSurplusMinor)
	}
	if got := s.MemberSurplusMinor[memberA] + s.MemberSurplusMinor[memberB]; got != 99_999 {
		t.Errorf("shares sum = %d, want 99999", got)
	}
}

func TestGoalTime(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// £4,200 of £10,000 at £400/month → ceil(5800/400) = 15 months.
	g := GoalTime(now, 420_000, 1_000_000, 40_000)
	if g == nil || g.Months != 15 {
		t.Fatalf("months = %v, want 15", g)
	}
	if !g.TargetDate.Equal(now.AddDate(0, 15, 0)) {
		t.Errorf("targetDate = %v", g.TargetDate)
	}

	if GoalTime(now, 100, 100, 50) != nil {
		t.Error("expected nil when already at target")
	}
	if GoalTime(now, 0, 100, 0) != nil {
		t.Error("expected nil with zero contribution")
	}
	// Exact division must not round up an extra month.
	if g := GoalTime(now, 0, 120_000, 40_000); g.Months != 3 {
		t.Errorf("months = %d, want 3", g.Months)
	}
}

func TestProjectedBalances(t *testing.T) {
	got := ProjectedBalances(100, 50, 3, 220)
	want := []int64{100, 150, 200, 220} // capped at target
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("projection = %v, want %v", got, want)
		}
	}
}

func TestMonthlyMinorRounding(t *testing.T) {
	// £100/week → 100*52/12 = £433.333… → rounds to £433.33
	if got := domain.MonthlyMinor(10_000, domain.FrequencyWeekly); got != 43_333 {
		t.Errorf("weekly = %d, want 43333", got)
	}
	// £1,000/year → £83.33 monthly
	if got := domain.MonthlyMinor(100_000, domain.FrequencyAnnual); got != 8_333 {
		t.Errorf("annual = %d, want 8333", got)
	}
	if got := domain.MonthlyMinor(5_000, domain.FrequencyMonthly); got != 5_000 {
		t.Errorf("monthly = %d, want 5000", got)
	}
}

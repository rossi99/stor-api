package finance

import (
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/domain"
)

// Pure calculation layer behind the summary and forecast endpoints. Ports the
// app's client-side maths (TimeToGoal.swift, FinancialPosition.swift) so both
// ends agree on the numbers. All amounts are minor units (pence).

type MemberIncome struct {
	UserID          uuid.UUID
	NetMonthlyMinor int64
}

type ExpenseLine struct {
	AmountMinor int64
	Frequency   domain.Frequency
	Category    domain.Category
}

type Summary struct {
	MonthlyIncomeMinor   int64
	MonthlyExpensesMinor int64
	MonthlySavingsMinor  int64
	NetSurplusMinor      int64
	Buckets              map[domain.Bucket]int64
	MemberSurplusMinor   map[uuid.UUID]int64
}

// Summarise computes the household financial position from approved expenses,
// member net incomes and pot contributions, splitting any surplus between
// members by the household's split method.
func Summarise(incomes []MemberIncome, expenses []ExpenseLine, savingsMinor int64, split domain.SplitMethod) Summary {
	s := Summary{
		MonthlySavingsMinor: savingsMinor,
		Buckets: map[domain.Bucket]int64{
			domain.BucketNeeds: 0, domain.BucketWants: 0, domain.BucketSavings: 0,
		},
		MemberSurplusMinor: make(map[uuid.UUID]int64, len(incomes)),
	}
	for _, inc := range incomes {
		s.MonthlyIncomeMinor += inc.NetMonthlyMinor
	}
	for _, e := range expenses {
		monthly := domain.MonthlyMinor(e.AmountMinor, e.Frequency)
		s.MonthlyExpensesMinor += monthly
		s.Buckets[e.Category.Bucket()] += monthly
	}
	s.NetSurplusMinor = s.MonthlyIncomeMinor - s.MonthlyExpensesMinor - s.MonthlySavingsMinor

	if len(incomes) > 0 {
		remaining := s.NetSurplusMinor
		for i, inc := range incomes {
			var share int64
			if i == len(incomes)-1 {
				share = remaining // last member absorbs rounding remainder
			} else if split == domain.SplitEven {
				share = s.NetSurplusMinor / int64(len(incomes))
			} else if s.MonthlyIncomeMinor > 0 {
				share = s.NetSurplusMinor * inc.NetMonthlyMinor / s.MonthlyIncomeMinor
			}
			s.MemberSurplusMinor[inc.UserID] = share
			remaining -= share
		}
	}
	return s
}

type TimeToGoal struct {
	Months     int       `json:"months"`
	TargetDate time.Time `json:"targetDate"`
}

// GoalTime ports timeToGoal() from TimeToGoal.swift: nil when the pot is
// already funded or nothing is being contributed.
func GoalTime(now time.Time, currentMinor, targetMinor, contributionMinor int64) *TimeToGoal {
	if contributionMinor <= 0 || currentMinor >= targetMinor {
		return nil
	}
	remaining := targetMinor - currentMinor
	months := int((remaining + contributionMinor - 1) / contributionMinor) // ceil
	return &TimeToGoal{Months: months, TargetDate: now.AddDate(0, months, 0)}
}

// ProjectedBalances ports projectedBalances(): a linear projection including
// the starting balance, capped at the target when one is given (>0).
func ProjectedBalances(currentMinor, contributionMinor int64, months int, targetMinor int64) []int64 {
	out := make([]int64, 0, months+1)
	for m := 0; m <= months; m++ {
		bal := currentMinor + contributionMinor*int64(m)
		if targetMinor > 0 && bal > targetMinor {
			bal = targetMinor
		}
		out = append(out, bal)
	}
	return out
}

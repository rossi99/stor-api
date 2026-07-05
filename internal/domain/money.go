package domain

// All amounts are integer minor units (pence). MaxAmountMinor bounds inputs to
// £100m, far above any plausible household figure, to catch garbage early.
const MaxAmountMinor int64 = 10_000_000_000

func ValidAmount(v int64) bool { return v >= 0 && v <= MaxAmountMinor }

// MonthlyMinor normalises an amount at the given frequency to a monthly
// figure, rounding half-up to the nearest penny (weekly ×52/12, annual ÷12) —
// the same normalisation as Expense.monthlyAmount in the app.
func MonthlyMinor(amount int64, f Frequency) int64 {
	switch f {
	case FrequencyWeekly:
		return roundDiv(amount*52, 12)
	case FrequencyAnnual:
		return roundDiv(amount, 12)
	default:
		return amount
	}
}

func roundDiv(n, d int64) int64 { return (n + d/2) / d }

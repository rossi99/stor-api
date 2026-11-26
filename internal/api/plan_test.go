package api

import (
	"reflect"
	"testing"
	"time"
)

func TestThisMonth(t *testing.T) {
	for _, c := range []struct {
		bill planBill
		want int64
	}{
		{planBill{Frequency: "monthly", Amount: 1200_00}, 1200_00},
		{planBill{Frequency: "weekly", Amount: 50_00}, 200_00},
		{planBill{Frequency: "quarterly", Amount: 100_00}, 33_33},
		{planBill{Frequency: "quarterly", Amount: 100_01}, 33_34},
		{planBill{Frequency: "yearly", Amount: 100_00}, 8_33},
		{planBill{Frequency: "yearly", Amount: 102}, 9},
		{planBill{Frequency: "monthly", Amount: 60_00, Months: []int16{3, 9}}, 0},
		{planBill{Frequency: "monthly", Amount: 60_00, Months: []int16{1, 9}}, 60_00},
	} {
		if got := thisMonth(c.bill, time.January); got != c.want {
			t.Errorf("thisMonth(%+v) = %d, want %d", c.bill, got, c.want)
		}
	}
}

func TestSplitStatus(t *testing.T) {
	for using, want := range map[int64]string{101: "over", 100: "left", 95: "left", 94: "spare", 0: "spare"} {
		if got := split("x", 100, 100, using).Status; got != want {
			t.Errorf("using %d of 100 = %q, want %q", using, got, want)
		}
	}
}

func budgetFixture() ([]int64, []planBill) {
	pays := []int64{3000_00, 2000_00}
	bills := []planBill{
		{ID: "rent", Name: "Rent", Group: "Home", Kind: "bill", Category: "essential", Frequency: "monthly", Amount: 1200_00},
		{ID: "water", Name: "Water", Group: "Home", Kind: "bill", Category: "essential", Frequency: "quarterly", Amount: 120_00},
		{ID: "cover", Name: "Insurance", Group: "Home", Kind: "bill", Category: "essential", Frequency: "yearly", Amount: 100_00},
		{ID: "fuel", Name: "Fuel", Group: "Fuel", Kind: "pot", Category: "essential", Frequency: "weekly", Amount: 50_00},
		{ID: "loan", Name: "Car loan", Group: "Loans", Kind: "loan", Category: "essential", Frequency: "monthly", Amount: 180_00},
		{ID: "tv", Name: "Streaming", Group: "Subscriptions", Kind: "bill", Category: "discretionary", Frequency: "monthly", Amount: 20_00},
		{ID: "annual", Name: "Annual sub", Group: "Subscriptions", Kind: "bill", Category: "discretionary", Frequency: "monthly", Amount: 60_00, Months: []int16{3}},
		{ID: "away", Name: "Holiday", Group: "Holiday", Kind: "saving", Category: "discretionary", Frequency: "monthly", Amount: 150_00},
		{ID: "rainy", Name: "Rainy day", Group: "Emergency", Kind: "saving", Category: "saving", Frequency: "monthly", Amount: 100_00},
		{ID: "isa", Name: "ISA", Group: "ISA", Kind: "investment", Category: "saving", Frequency: "monthly", Amount: 100_00},
		{ID: "gym", Name: "Gym", Frequency: "monthly", Amount: 30_00, PaidFrom: 1},
		{ID: "phone", Name: "Phone", Frequency: "monthly", Amount: 25_00, PaidFrom: 2},
	}
	return pays, bills
}

func TestPlanCapped(t *testing.T) {
	pays, bills := budgetFixture()
	limit := int64(300_00)
	settings := budgetSettings{EssentialPct: 50, DiscretionaryPct: 30, SavingPct: 20, LoanAllocation: 250_00, CashCap: &limit}
	p := buildPlan(pays, settings, bills, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))

	got := []int64{p.Income, p.LoanRepayments, p.LoanOverpay, p.FixedInvesting, p.BillsAndPots, p.AddInvesting, p.CashEach, p.Stays}
	if want := []int64{5000_00, 180_00, 70_00, 100_00, 1968_33, 2331_67, 300_00, 300_00}; !reflect.DeepEqual(got, want) {
		t.Fatalf("totals = %v, want %v", got, want)
	}

	wantTransfers := []planTransfer{
		{Key: "pay:2", Kind: "payIn", Slot: 2, Amount: 2000_00},
		{Key: "pot:Home", Kind: "pot", Group: "Home", Amount: 1248_33},
		{Key: "pot:Loans", Kind: "pot", Group: "Loans", Amount: 250_00},
		{Key: "pot:Fuel", Kind: "pot", Group: "Fuel", Amount: 200_00},
		{Key: "pot:Subscriptions", Kind: "pot", Group: "Subscriptions", Amount: 20_00},
		{Key: "pot:Holiday", Kind: "pot", Group: "Holiday", Amount: 150_00},
		{Key: "pot:Emergency", Kind: "pot", Group: "Emergency", Amount: 100_00},
		{Key: "investing:ISA", Kind: "investing", Group: "ISA", Amount: 2431_67},
		{Key: "cash:2", Kind: "cash", Slot: 2, Amount: 300_00},
	}
	if !reflect.DeepEqual(p.Transfers, wantTransfers) {
		t.Fatalf("transfers = %+v", p.Transfers)
	}

	wantGroups := []planRow{
		{"Investing", 2431_67}, {"Home", 1248_33}, {"Cash accounts", 600_00}, {"Loans", 250_00},
		{"Savings pots", 250_00}, {"Fuel", 200_00}, {"Subscriptions", 20_00},
	}
	if !reflect.DeepEqual(p.Groups, wantGroups) {
		t.Fatalf("groups = %v", p.Groups)
	}

	wantSplit := []planSplit{
		{"essential", 2500_00, 2500_00, 1628_33, "spare"},
		{"discretionary", 1500_00, 1500_00, 170_00, "spare"},
		{"saving", 1000_00, 2330_00, 270_00, "spare"},
	}
	if !reflect.DeepEqual(p.Split, wantSplit) {
		t.Fatalf("split = %v", p.Split)
	}

	var biggest []string
	for _, b := range p.Biggest {
		biggest = append(biggest, b.ID)
	}
	if want := []string{"rent", "fuel", "loan", "water", "tv", "cover"}; !reflect.DeepEqual(biggest, want) {
		t.Fatalf("biggest = %v", biggest)
	}

	if want := []planCash{{1, 300_00, 30_00, 270_00}, {2, 300_00, 25_00, 275_00}}; !reflect.DeepEqual(p.Cash, want) {
		t.Fatalf("cash = %v", p.Cash)
	}

	if len(p.Forecast) != 12 || p.Forecast[11].Month != time.Date(2027, time.December, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("forecast months = %v", p.Forecast)
	}
	jan, mar := p.Forecast[0], p.Forecast[2]
	if jan.Bills != 1518_33 || jan.PotsAndSavings != 450_00 || jan.AddInvesting != 2331_67 || jan.CashEach != 300_00 ||
		len(jan.ExtraBills) != 0 || jan.IsShort {
		t.Fatalf("January = %+v", jan)
	}
	if mar.Bills != 1578_33 || mar.AddInvesting != 2271_67 || !reflect.DeepEqual(mar.ExtraBills, []string{"Annual sub"}) {
		t.Fatalf("March = %+v", mar)
	}

	limit = 2000_00
	if p := buildPlan(pays, settings, bills, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)); p.AddInvesting != 0 ||
		p.CashEach != 1465_83 || !p.Forecast[0].IsShort {
		t.Fatalf("a cap above what is left: add. investing %d, cash %d, short %v", p.AddInvesting, p.CashEach, p.Forecast[0].IsShort)
	}
}

func TestPlanUncappedAndOverspent(t *testing.T) {
	pays, bills := budgetFixture()
	settings := budgetSettings{EssentialPct: 30, DiscretionaryPct: 30, SavingPct: 40}
	p := buildPlan(pays, settings, bills, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))

	// Without a cap nothing goes to add. investing, and the odd penny stays with the hub.
	if p.LoanOverpay != 0 || p.AddInvesting != 0 || p.CashEach != 1500_83 || p.Stays != 1500_84 {
		t.Fatalf("overpay %d, add. investing %d, cash %d, stays %d", p.LoanOverpay, p.AddInvesting, p.CashEach, p.Stays)
	}
	// Essential is over by £128.33, which comes out of discretionary before saving gets the spare.
	wantSplit := []planSplit{
		{"essential", 1500_00, 1500_00, 1628_33, "over"},
		{"discretionary", 1500_00, 1371_67, 170_00, "spare"},
		{"saving", 2000_00, 3201_67, 200_00, "spare"},
	}
	if !reflect.DeepEqual(p.Split, wantSplit) {
		t.Fatalf("split = %v", p.Split)
	}

	// Bills beyond income leave each person short, and no negative transfer goes out.
	short := budgetSettings{EssentialPct: 50, DiscretionaryPct: 30, SavingPct: 20}
	p = buildPlan([]int64{1000_00}, short, bills, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))
	if p.CashEach >= 0 || p.Stays >= 0 || p.Cash[0].Left >= 0 {
		t.Fatalf("overspent household: cash %d, stays %d", p.CashEach, p.Stays)
	}
	for _, tr := range p.Transfers {
		if tr.Amount <= 0 || tr.Kind == "cash" || tr.Kind == "payIn" {
			t.Fatalf("unexpected transfer for a household of one: %+v", tr)
		}
	}
}

func TestPlanLoanAllocationWithoutLoans(t *testing.T) {
	settings := budgetSettings{EssentialPct: 50, DiscretionaryPct: 30, SavingPct: 20, LoanAllocation: 300_00}
	p := buildPlan([]int64{2000_00}, settings, nil, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))
	if want := []planTransfer{{Key: "pot:Loans & debts", Kind: "pot", Group: "Loans & debts", Amount: 300_00}}; !reflect.DeepEqual(p.Transfers, want) {
		t.Fatalf("transfers = %+v", p.Transfers)
	}
	if p.Split[2].Using != 300_00 || p.CashEach != 1700_00 || len(p.Biggest) != 0 {
		t.Fatalf("saving using %d, cash %d, biggest %v", p.Split[2].Using, p.CashEach, p.Biggest)
	}
}

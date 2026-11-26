package main

import (
	"reflect"
	"strings"
	"testing"
)

const sheet = `,Groups,,,Frequency,Category
,Household & utilities,Pets,Savings pots,Monthly,Essential
,,,,,,,,,,
,Bill,Group,Amount,Frequency,Category,Months,This month,,,
,Household & Utilities,,,,,,,,,
,Covers all payments regarding home things,,,,,,,,,
,Rent,Household & utilities,"£1,234.50",Monthly,Essential,,"£1,234.50",,,
,Fuel top-up,Fuel,£60.00,Weekly,Essential,,£240.00,,,
,Car tax,Cars,£20.00,Monthly,Essential,"Jan, sept",£0.00,,,
,Car tax,Cars,£25.00,Monthly,Essential,,£25.00,,,
,Phone loan,Loans & debts,£36.50,Monthly,Essential,,£36.50,,,
,,,,,,Total:,"£1,577.62",,,
,Holiday,Savings pots,£364.00,Monthly,Discretionary,,£364.00,,,
,Stocks & shares,Investing,£100.00,Monthly,Saving,,£100.00,,,
,,,,,,,,,,
,Cash accounts,,,,,,,,,
,This month,Ross,Nez,,,,,,,
,Cash account,£400.00,£400.00,,,,,,,
,Bill,Paid from,Amount,Frequency,Notes,Months,This month,,,
,PS5 add. payment,Ross's cash account,£16.66,Monthly,,,£16.66,,,
`

func TestParseBills(t *testing.T) {
	got, err := parseBills(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	want := []bill{
		{ID: "sheet-household-utilities-rent", Name: "Rent", Group: "Household & utilities", Kind: "bill", Category: "essential", Amount: 1234.5, Frequency: "monthly", Months: []int{}, PaidFrom: "joint"},
		{ID: "sheet-fuel-fuel-top-up", Name: "Fuel top-up", Group: "Fuel", Kind: "pot", Category: "essential", Amount: 60, Frequency: "weekly", Months: []int{}, PaidFrom: "joint"},
		{ID: "sheet-cars-car-tax", Name: "Car tax", Group: "Cars", Kind: "bill", Category: "essential", Amount: 20, Frequency: "monthly", Months: []int{1, 9}, PaidFrom: "joint"},
		{ID: "sheet-cars-car-tax-2", Name: "Car tax", Group: "Cars", Kind: "bill", Category: "essential", Amount: 25, Frequency: "monthly", Months: []int{}, PaidFrom: "joint"},
		{ID: "sheet-loans-debts-phone-loan", Name: "Phone loan", Group: "Loans & debts", Kind: "loan", Category: "essential", Amount: 36.5, Frequency: "monthly", Months: []int{}, PaidFrom: "joint"},
		{ID: "sheet-holiday-holiday", Name: "Holiday", Group: "Holiday", Kind: "saving", Category: "discretionary", Amount: 364, Frequency: "monthly", Months: []int{}, PaidFrom: "joint"},
		{ID: "sheet-investing-stocks-shares", Name: "Stocks & shares", Group: "Investing", Kind: "investment", Category: "saving", Amount: 100, Frequency: "monthly", Months: []int{}, PaidFrom: "joint"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseBillsRefusesWhatItCannotRead(t *testing.T) {
	for _, csv := range []string{
		",Bill,Group,Amount,Frequency,Category,Months\n,Rent,Home,lots,Monthly,Essential,\n",
		",Bill,Group,Amount,Frequency,Category,Months\n,Rent,Home,£5.00,Monthly,Essential,Smarch\n",
		",Bill,Group,Amount,Frequency,Category,Months\n,Rent,Home,£5.00,Fortnightly,Essential,\n",
		",Bill,Group,Amount,Frequency,Category,Months\n,Rent,Home,£5.00,Monthly,Needs,\n",
		",Bill,Amount\n,Rent,£5.00\n",
	} {
		if _, err := parseBills(strings.NewReader(csv)); err == nil {
			t.Errorf("accepted %q", csv)
		}
	}
}

// Command import loads the Bills table from a CSV export of the budget sheet's Monthly Budget tab
// into a Stór household, through the API so its validation applies.
//
//	STOR_PASSWORD=… go run ./cmd/import -email you@example.com -csv monthly-budget.csv
//
// Re-running it updates the same bills rather than adding copies.
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type bill struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Group     string  `json:"group"`
	Kind      string  `json:"kind"`
	Category  string  `json:"category"`
	Amount    float64 `json:"amount"`
	Frequency string  `json:"frequency"`
	Months    []int   `json:"months"`
	PaidFrom  string  `json:"paidFrom"`
	Meta      string  `json:"meta"`
}

// shortcut: the sheet's own group names decide each bill's kind; make this a flag if another sheet names them differently.
var kinds = map[string]string{
	"loans & debts": "loan", "investing": "investment", "savings pots": "saving",
	"fuel": "pot", "groceries": "pot", "dip pot": "pot",
}

var (
	monthNames  = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	frequencies = []string{"monthly", "weekly", "quarterly", "yearly"}
	categories  = []string{"essential", "discretionary", "saving"}
)

// parseBills reads the joint Bills table: it starts at the Bill/Group/Amount header and ends at the
// Cash accounts section. Headings, notes and totals have no group and are passed over.
func parseBills(r io.Reader) ([]bill, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	var col map[string]int
	var out []bill
	ids := map[string]int{}
	for line, record := range records {
		cell := func(name string) string {
			if i, ok := col[name]; ok && i < len(record) {
				return strings.TrimSpace(record[i])
			}
			return ""
		}
		if col == nil {
			if header := headerColumns(record); header != nil {
				col = header
			}
			continue
		}
		name, group := cell("Bill"), cell("Group")
		if name == "Bill" || strings.EqualFold(name, "Cash accounts") {
			break
		}
		if name == "" || group == "" {
			continue
		}
		amount, amountOK := money(cell("Amount"))
		months, monthsOK := parseMonths(cell("Months"))
		frequency, category := strings.ToLower(cell("Frequency")), strings.ToLower(cell("Category"))
		if !amountOK || !monthsOK || !slices.Contains(frequencies, frequency) || !slices.Contains(categories, category) {
			return nil, fmt.Errorf("line %d (%s): can't read its amount, frequency, category or months", line+1, name)
		}
		kind := kinds[strings.ToLower(group)]
		if kind == "" {
			kind = "bill"
		}
		if kind == "saving" {
			// Each savings pot is its own transfer, so it becomes its own group.
			group = name
		}
		id := slug("sheet-" + group + "-" + name)
		if ids[id]++; ids[id] > 1 {
			id = fmt.Sprintf("%s-%d", id, ids[id])
		}
		out = append(out, bill{
			ID: id, Name: name, Group: group, Kind: kind, Category: category, Amount: amount,
			Frequency: frequency, Months: months, PaidFrom: "joint",
		})
	}
	if col == nil {
		return nil, errors.New("no Bill, Group, Amount, Frequency, Category, Months header found")
	}
	return out, nil
}

func headerColumns(record []string) map[string]int {
	col := map[string]int{}
	for i, cell := range record {
		col[strings.TrimSpace(cell)] = i
	}
	for _, name := range []string{"Bill", "Group", "Amount", "Frequency", "Category", "Months"} {
		if _, ok := col[name]; !ok {
			return nil
		}
	}
	return col
}

// money reads the sheet's displayed amounts, like "£1,225.05".
func money(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.NewReplacer("£", "", ",", "", " ", "").Replace(s), 64)
	return math.Round(f*100) / 100, err == nil
}

// parseMonths reads "Feb, May, Aug" as [2 5 8]; empty means every month.
func parseMonths(s string) ([]int, bool) {
	months := []int{}
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part == "" {
			continue
		}
		i := slices.Index(monthNames, part[:min(3, len(part))])
		if i < 0 {
			return nil, false
		}
		months = append(months, i+1)
	}
	return months, true
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	// 56 leaves room for a "-2" suffix inside the API's 64-character ids.
	out := strings.Trim(b.String(), "-")
	return out[:min(56, len(out))]
}

var client = &http.Client{Timeout: 10 * time.Second}

func send(method, endpoint, token string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

func run(api, email, password, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bills, err := parseBills(f)
	if err != nil {
		return err
	}

	res, err := send("POST", api+"/auth/sign-in", "", map[string]string{"email": email, "password": password})
	if err != nil {
		return err
	}
	var session struct{ Token string }
	err = json.NewDecoder(res.Body).Decode(&session)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || err != nil {
		return fmt.Errorf("sign-in failed (%d)", res.StatusCode)
	}

	for _, b := range bills {
		res, err := send("PUT", api+"/bills/"+url.PathEscape(b.ID), session.Token, b)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			return fmt.Errorf("%s was refused (%d)", b.Name, res.StatusCode)
		}
	}
	fmt.Printf("Imported %d bills.\n", len(bills))
	return nil
}

func main() {
	api := flag.String("api", "http://localhost:8080", "Stór API base URL")
	email := flag.String("email", "", "account to import into")
	path := flag.String("csv", "", "CSV export of the Monthly Budget tab")
	flag.Parse()
	password := os.Getenv("STOR_PASSWORD")
	if *email == "" || *path == "" || password == "" {
		fmt.Fprintln(os.Stderr, "usage: STOR_PASSWORD=… import -email you@example.com -csv monthly-budget.csv [-api URL]")
		os.Exit(2)
	}
	if err := run(strings.TrimSuffix(*api, "/"), *email, password, *path); err != nil {
		fmt.Fprintln(os.Stderr, "import:", err)
		os.Exit(1)
	}
}

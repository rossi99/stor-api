package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rossi99/stor/internal/store"
)

func TestPassword(t *testing.T) {
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("correct horse", hash) || verifyPassword("correct horsf", hash) || verifyPassword("x", "garbage") {
		t.Fatal("password verification is wrong")
	}
}

func TestPence(t *testing.T) {
	for in, want := range map[float64]int64{0.1 + 0.2: 30, 12.345: 1235, -4.99: -499} {
		if got, ok := pence(in); !ok || got != want {
			t.Errorf("pence(%v) = %d, want %d", in, got, want)
		}
	}
	if _, ok := pence(1e10); ok {
		t.Error("accepted £10bn")
	}
}

type client struct {
	t   *testing.T
	url string
}

func (c client) do(method, path, token string, body any) (int, map[string]any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.url+path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (c client) expect(want int, method, path, token string, body any) map[string]any {
	c.t.Helper()
	got, out := c.do(method, path, token, body)
	if got != want {
		c.t.Fatalf("%s %s = %d, want %d (%v)", method, path, got, want, out)
	}
	return out
}

func setup(t *testing.T) (client, *pgxpool.Pool) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run `task test`")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE users, households CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(pool))
	t.Cleanup(srv.Close)
	return client{t, srv.URL}, pool
}

func signUp(c client, email string) string {
	return c.expect(201, "POST", "/auth/sign-up", "", map[string]string{"email": email, "password": "password-1"})["token"].(string)
}

func TestAuth(t *testing.T) {
	c, pool := setup(t)
	token := signUp(c, "Ana@Example.com")
	c.expect(409, "POST", "/auth/sign-up", "", map[string]string{"email": "ana@example.com ", "password": "password-2"})
	c.expect(400, "POST", "/auth/sign-up", "", map[string]string{"email": "sam@example.com", "password": "short"})
	c.expect(401, "POST", "/auth/sign-in", "", map[string]string{"email": "ana@example.com", "password": "wrong-pass"})
	c.expect(401, "POST", "/auth/sign-in", "", map[string]string{"email": "nobody@example.com", "password": "password-1"})

	session := c.expect(200, "POST", "/auth/sign-in", "", map[string]string{"email": "ANA@example.com", "password": "password-1"})
	if session["email"] != "ana@example.com" || session["hasCompletedSetup"] != false || session["token"] == token {
		t.Fatalf("sign-in session = %v", session)
	}

	c.expect(401, "GET", "/household", "", nil)
	c.expect(401, "GET", "/household", "not-a-token", nil)
	c.expect(204, "POST", "/household/setup-complete", token, nil)
	session = c.expect(200, "POST", "/auth/sign-in", "", map[string]string{"email": "ana@example.com", "password": "password-1"})
	if session["hasCompletedSetup"] != true {
		t.Fatal("setup-complete did not persist")
	}

	if _, err := pool.Exec(context.Background(), `UPDATE sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	c.expect(401, "GET", "/household", token, nil)
}

func TestNewHousehold(t *testing.T) {
	c, _ := setup(t)
	token := signUp(c, "ana@example.com")
	req, _ := http.NewRequest("GET", c.url+"/household", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	body := string(raw)
	for _, key := range []string{`"bills":[]`, `"recaps":[]`, `"linkedAccountIDs":[]`, `"transactions":[]`, `"goals":[]`} {
		if !strings.Contains(body, key) {
			t.Errorf("missing %s in %s", key, body)
		}
	}
	if strings.Contains(body, "netWorth") || strings.Contains(body, "emergencyFund") {
		t.Errorf("unknown optionals should be omitted: %s", body)
	}
	var snap struct{ Members []member }
	json.Unmarshal(raw, &snap)
	if len(snap.Members) != 2 || snap.Members[0].Ledger != "you" || snap.Members[1].Ledger != "partner" || snap.Members[0].Name != "" {
		t.Fatalf("members = %+v", snap.Members)
	}
}

func txn(id, ledger string, amount float64) map[string]any {
	return map[string]any{
		"id": id, "date": time.Now().UTC().Format(time.RFC3339), "title": "Tesco " + id, "category": "Groceries",
		"amount": amount, "actor": "you", "actorName": "Ana", "ledger": ledger, "tag": "Joint", "monogram": "TE",
	}
}

func transactions(c client, token string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, t := range c.expect(200, "GET", "/household", token, nil)["transactions"].([]any) {
		row := t.(map[string]any)
		out[row["id"].(string)] = row
	}
	return out
}

func TestTransactionsIdempotentAndPrivate(t *testing.T) {
	c, pool := setup(t)
	ctx := context.Background()
	ana := signUp(c, "ana@example.com")
	sam := signUp(c, "sam@example.com")
	// No join endpoint exists yet, so move Sam into Ana's household as the second member.
	_, err := pool.Exec(ctx, `
		WITH ana AS (SELECT m.household_id FROM members m JOIN users u ON u.id = m.user_id WHERE u.email = 'ana@example.com'),
		     sam AS (DELETE FROM members WHERE user_id = (SELECT id FROM users WHERE email = 'sam@example.com') RETURNING user_id)
		UPDATE members SET user_id = (SELECT user_id FROM sam) WHERE household_id = (SELECT household_id FROM ana) AND slot = 2`)
	if err != nil {
		t.Fatal(err)
	}

	c.expect(204, "POST", "/transactions", ana, txn("a-own", "you", -12.5))
	c.expect(204, "POST", "/transactions", ana, txn("a-own", "you", -12.5))
	c.expect(204, "POST", "/transactions", ana, txn("joint", "joint", -40))
	c.expect(204, "POST", "/transactions", sam, txn("s-own", "you", -7.25))
	c.expect(400, "POST", "/transactions", ana, txn("bad", "theirs", -1))
	c.expect(400, "POST", "/transactions", ana, txn("", "you", -1))

	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE id = 'a-own'`).Scan(&count)
	if count != 1 {
		t.Fatalf("retried POST stored %d rows", count)
	}

	anaSees := transactions(c, ana)
	if _, ok := anaSees["s-own"]; ok || len(anaSees) != 2 || anaSees["a-own"]["ledger"] != "you" {
		t.Fatalf("Ana sees %v", anaSees)
	}
	if date := anaSees["a-own"]["date"].(string); strings.Contains(date, ".") || !strings.HasSuffix(date, "Z") {
		t.Fatalf("date %q has fractional seconds or no zone", date)
	}
	samSees := transactions(c, sam)
	if _, ok := samSees["a-own"]; ok || samSees["s-own"]["ledger"] != "you" || samSees["s-own"]["actor"] != "you" || samSees["joint"] == nil {
		t.Fatalf("Sam sees %v", samSees)
	}

	// Totals stay visible even though the partner's rows do not.
	ledgers := c.expect(200, "GET", "/household", ana, nil)["ledgers"].([]any)
	spent := map[string]float64{}
	for _, l := range ledgers {
		spent[l.(map[string]any)["ledger"].(string)] = l.(map[string]any)["spent"].(float64)
	}
	if spent["you"] != 12.5 || spent["joint"] != 40 || spent["partner"] != 7.25 {
		t.Fatalf("spent = %v", spent)
	}
}

func TestHouseholdSettings(t *testing.T) {
	c, pool := setup(t)
	token := signUp(c, "ana@example.com")

	c.expect(204, "PUT", "/household/contributions", token, []map[string]any{{"ledger": "you", "amount": 500}, {"ledger": "partner", "amount": 312.34}})
	c.expect(400, "PUT", "/household/contributions", token, []map[string]any{{"ledger": "joint", "amount": 1}})
	c.expect(400, "PUT", "/household/contributions", token, []map[string]any{{"ledger": "you", "amount": -1}})
	c.expect(204, "PUT", "/household/top-up-day", token, map[string]int{"day": 15})
	c.expect(400, "PUT", "/household/top-up-day", token, map[string]int{"day": 29})
	c.expect(204, "PUT", "/household/linked-accounts", token, map[string][]string{"ids": {}})
	c.expect(400, "PUT", "/household/linked-accounts", token, map[string][]string{"ids": {"acc-1"}})

	c.expect(404, "PUT", "/goals/holiday/monthly", token, map[string]float64{"monthly": 100})
	if _, err := pool.Exec(context.Background(), `INSERT INTO goals (household_id, id, name, target_pence) SELECT household_id, 'holiday', 'Holiday', 200000 FROM members WHERE slot = 1`); err != nil {
		t.Fatal(err)
	}
	c.expect(204, "PUT", "/goals/holiday/monthly", token, map[string]float64{"monthly": 150})

	snap := c.expect(200, "GET", "/household", token, nil)
	members := snap["members"].([]any)
	if snap["topUpDay"] != 15.0 || members[0].(map[string]any)["contribution"] != 500.0 || members[1].(map[string]any)["contribution"] != 312.34 {
		t.Fatalf("snapshot = %v", snap)
	}
	if g := snap["goals"].([]any)[0].(map[string]any); g["monthly"] != 150.0 || g["target"] != 2000.0 {
		t.Fatalf("goal = %v", g)
	}
}

func TestLedgerMappingRoundTrips(t *testing.T) {
	for size := int16(1); size <= maxMembers; size++ {
		for slot := int16(1); slot <= size; slot++ {
			v := viewer{slot: slot, size: size}
			seen := map[string]bool{}
			for s := int16(0); s <= size; s++ {
				ledger := v.ledgerFor(s)
				if back, ok := v.slotFor(ledger); !ok || back != s || seen[ledger] {
					t.Fatalf("size %d viewer %d: slot %d -> %q -> %d, %v", size, slot, s, ledger, back, ok)
				}
				seen[ledger] = true
			}
			for _, ledger := range otherLedgers[size-1:] {
				if _, ok := v.slotFor(ledger); ok {
					t.Fatalf("size %d viewer %d accepted %q", size, slot, ledger)
				}
			}
		}
	}
}

func ledgerNames(c client, token string) (members, ledgers []string) {
	snap := c.expect(200, "GET", "/household", token, nil)
	for _, m := range snap["members"].([]any) {
		members = append(members, m.(map[string]any)["ledger"].(string))
	}
	for _, l := range snap["ledgers"].([]any) {
		ledgers = append(ledgers, l.(map[string]any)["ledger"].(string))
	}
	return members, ledgers
}

func TestHouseholdSize(t *testing.T) {
	c, _ := setup(t)
	token := signUp(c, "ana@example.com")

	c.expect(400, "PUT", "/household/size", token, map[string]int{"size": 0})
	c.expect(400, "PUT", "/household/size", token, map[string]int{"size": 5})
	c.expect(204, "PUT", "/household/size", token, map[string]int{"size": 4})
	members, ledgers := ledgerNames(c, token)
	if strings.Join(members, ",") != "you,partner,third,fourth" || strings.Join(ledgers, ",") != "you,joint,partner,third,fourth" {
		t.Fatalf("members %v ledgers %v", members, ledgers)
	}
	c.expect(204, "PUT", "/household/contributions", token, []map[string]any{{"ledger": "fourth", "amount": 25}})

	// Another member's personal spending is counted but never listed.
	c.expect(204, "POST", "/transactions", token, txn("t-third", "third", -9))
	if _, ok := transactions(c, token)["t-third"]; ok {
		t.Fatal("third member's personal transaction leaked")
	}
	c.expect(409, "PUT", "/household/size", token, map[string]int{"size": 2})
	c.expect(204, "PUT", "/household/size", token, map[string]int{"size": 3})
	if members, _ := ledgerNames(c, token); len(members) != 3 {
		t.Fatalf("after shrink members = %v", members)
	}
	c.expect(400, "POST", "/transactions", token, txn("t-fourth", "fourth", -1))
	c.expect(400, "PUT", "/household/contributions", token, []map[string]any{{"ledger": "fourth", "amount": 1}})

	solo := signUp(c, "sam@example.com")
	c.expect(204, "PUT", "/household/size", solo, map[string]int{"size": 1})
	if members, ledgers := ledgerNames(c, solo); strings.Join(members, ",") != "you" || strings.Join(ledgers, ",") != "you,joint" {
		t.Fatalf("solo members %v ledgers %v", members, ledgers)
	}
	c.expect(400, "POST", "/transactions", solo, txn("t-partner", "partner", -1))
	c.expect(204, "PUT", "/household/size", solo, map[string]int{"size": 2})
}

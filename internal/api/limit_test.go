package api

import (
	"context"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	clock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return clock }
	if !l.allow("a") || !l.allow("a") || l.allow("a") {
		t.Fatal("a burst of 2 should allow exactly two")
	}
	if !l.allow("b") {
		t.Fatal("keys are limited separately")
	}
	clock = clock.Add(30 * time.Second)
	if l.allow("a") {
		t.Fatal("half a refill is not a request")
	}
	clock = clock.Add(31 * time.Second)
	if !l.allow("a") || l.allow("a") {
		t.Fatal("one refill per minute")
	}
}

func TestPasswordGuessingIsLimited(t *testing.T) {
	c, _ := setup(t)
	for i := range 20 {
		if got, _ := c.do("POST", "/auth/sign-in", "", map[string]string{"email": "x@example.com", "password": "guess"}); got != 401 {
			t.Fatalf("attempt %d = %d", i+1, got)
		}
	}
	c.expect(429, "POST", "/auth/sign-in", "", map[string]string{"email": "x@example.com", "password": "guess"})
	c.expect(429, "POST", "/auth/sign-up", "", map[string]string{"email": "new@example.com", "password": "password-1"})
}

func TestDeleteAccount(t *testing.T) {
	c, pool := setup(t)
	ctx := context.Background()
	count := func(query string) (n int) {
		if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	solo := signUp(c, "solo@example.com")
	c.expect(204, "PUT", "/bills/rent", solo, bill("rent", "Home", "bill", 900))
	c.expect(401, "DELETE", "/auth/account", solo, map[string]string{"password": "wrong-pass"})
	c.expect(204, "DELETE", "/auth/account", solo, map[string]string{"password": "password-1"})
	c.expect(401, "GET", "/household", solo, nil)
	if count(`SELECT count(*) FROM households`) != 0 || count(`SELECT count(*) FROM users`) != 0 || count(`SELECT count(*) FROM bills`) != 0 {
		t.Fatal("a sole member's household should be deleted with them")
	}

	ana := signUp(c, "ana@example.com")
	sam := signUp(c, "sam@example.com")
	join(c, sam, invite(c, ana))
	c.expect(204, "PUT", "/household/members", ana, []map[string]any{{"ledger": "partner", "name": "Sam", "netPay": 2000}})
	c.expect(204, "PUT", "/bills/rent", sam, bill("rent", "Home", "bill", 900))
	c.expect(204, "PUT", "/bills/gym", sam, with(bill("gym", "", "bill", 30), "paidFrom", "you"))
	c.expect(204, "POST", "/transactions", sam, txn("s-own", "you", -5))
	c.expect(204, "POST", "/transactions", sam, txn("s-joint", "joint", -9))
	c.expect(204, "DELETE", "/auth/account", sam, map[string]string{"password": "password-1"})

	got := members(c, ana)
	if got[1]["hasAccount"] != false || got[1]["name"] != "" || got[1]["netPay"] != 0.0 {
		t.Fatalf("Sam's seat after deletion = %v", got[1])
	}
	if ids := billIDs(planFor(c, ana, "")); len(ids) != 1 || ids["rent"] == nil {
		t.Fatalf("joint bills should stay, personal ones go: %v", ids)
	}
	if count(`SELECT count(*) FROM bills WHERE paid_from = 2`) != 0 || count(`SELECT count(*) FROM transactions WHERE id = 's-own'`) != 0 ||
		count(`SELECT count(*) FROM transactions WHERE id = 's-joint' AND actor_name IS NULL`) != 1 {
		t.Fatal("Sam's personal data should be gone and the joint spend kept without a name")
	}
	c.expect(201, "POST", "/household/invites", ana, nil)
}

func TestJointBillsStayJoint(t *testing.T) {
	c, pool := setup(t)
	ana := signUp(c, "ana@example.com")
	c.expect(204, "PUT", "/bills/rent", ana, bill("rent", "Home", "bill", 900))
	c.expect(409, "PUT", "/bills/rent", ana, with(bill("rent", "", "bill", 900), "paidFrom", "you"))
	c.expect(204, "PUT", "/bills/gym", ana, with(bill("gym", "", "bill", 30), "paidFrom", "you"))
	c.expect(204, "PUT", "/bills/gym", ana, bill("gym", "Health", "bill", 30))
	c.expect(400, "PUT", "/plan/transfers", ana, map[string]any{"month": "2027-01", "key": "anything", "done": true})

	if _, err := pool.Exec(context.Background(), `
		INSERT INTO bills (household_id, id, name, group_name, kind, category, amount_pence, frequency, paid_from)
		SELECT m.household_id, 'b' || n, 'Bill', 'Home', 'bill', 'essential', 100, 'monthly', 0
		FROM members m, generate_series(1, 498) n WHERE m.user_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	c.expect(409, "PUT", "/bills/one-more", ana, bill("one-more", "Home", "bill", 1))
	c.expect(204, "PUT", "/bills/rent", ana, bill("rent", "Home", "bill", 950))
}

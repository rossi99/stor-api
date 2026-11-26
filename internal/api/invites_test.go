package api

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

func invite(c client, token string) string {
	return c.expect(201, "POST", "/household/invites", token, nil)["code"].(string)
}

func join(c client, token, code string) map[string]any {
	return c.expect(200, "POST", "/household/join", token, map[string]string{"code": code})
}

func members(c client, token string) []map[string]any {
	var out []map[string]any
	for _, m := range c.expect(200, "GET", "/household", token, nil)["members"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestMemberProfiles(t *testing.T) {
	c, _ := setup(t)
	ana := signUp(c, "ana@example.com")

	c.expect(204, "PUT", "/household/members", ana, []map[string]any{
		{"ledger": "you", "name": "  Ana María Ruiz ", "netPay": 3580},
		{"ledger": "partner", "name": "Sam", "netPay": 2182.5},
	})
	for _, bad := range []map[string]any{
		{"ledger": "joint", "name": "Pot", "netPay": 1},
		{"ledger": "third", "name": "Nobody", "netPay": 1},
		{"ledger": "you", "name": "Ana", "netPay": -1},
		{"ledger": "you", "name": strings.Repeat("a", 61), "netPay": 1},
	} {
		c.expect(400, "PUT", "/household/members", ana, []map[string]any{bad})
	}

	got := members(c, ana)
	if got[0]["name"] != "Ana María Ruiz" || got[0]["initials"] != "AR" || got[0]["netPay"] != 3580.0 || got[0]["hasAccount"] != true {
		t.Fatalf("you = %v", got[0])
	}
	if got[1]["name"] != "Sam" || got[1]["initials"] != "S" || got[1]["netPay"] != 2182.5 || got[1]["hasAccount"] != false {
		t.Fatalf("partner = %v", got[1])
	}
}

func TestInviteAndJoin(t *testing.T) {
	c, pool := setup(t)
	ctx := context.Background()
	ana := signUp(c, "ana@example.com")
	sam := signUp(c, "sam@example.com")
	c.expect(204, "PUT", "/household/members", ana, []map[string]any{{"ledger": "partner", "name": "Sam", "netPay": 2000}})
	c.expect(204, "POST", "/household/setup-complete", ana, nil)

	res := c.expect(201, "POST", "/household/invites", ana, nil)
	code := res["code"].(string)
	if !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	expires, err := time.Parse(time.RFC3339, res["expiresAt"].(string))
	if err != nil || time.Until(expires) < 47*time.Hour || time.Until(expires) > 49*time.Hour {
		t.Fatalf("expiresAt %v (%v)", res["expiresAt"], err)
	}

	c.expect(409, "POST", "/household/join", ana, map[string]string{"code": code})
	c.expect(404, "POST", "/household/join", sam, map[string]string{"code": "AAAA-AAAA"})
	session := join(c, sam, strings.ToLower(strings.ReplaceAll(code, "-", " ")))
	if session["email"] != "sam@example.com" || session["hasCompletedSetup"] != true {
		t.Fatalf("join session = %v", session)
	}

	// Sam takes the named empty seat, and the household sign-up made for Sam is gone.
	got := members(c, sam)
	if got[0]["ledger"] != "you" || got[0]["name"] != "Sam" || got[1]["ledger"] != "partner" || got[1]["hasAccount"] != true {
		t.Fatalf("Sam sees %v", got)
	}
	var households int
	pool.QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&households)
	if households != 1 {
		t.Fatalf("%d households after join", households)
	}

	cara := signUp(c, "cara@example.com")
	c.expect(404, "POST", "/household/join", cara, map[string]string{"code": code})
	c.expect(409, "POST", "/household/invites", ana, nil)

	c.expect(204, "PUT", "/household/size", ana, map[string]int{"size": 3})
	expired := invite(c, ana)
	if _, err := pool.Exec(ctx, `UPDATE invites SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	c.expect(404, "POST", "/household/join", cara, map[string]string{"code": expired})

	// A household with anything in it is never discarded, and the refused code stays usable.
	fresh := invite(c, ana)
	c.expect(204, "POST", "/transactions", cara, txn("c-1", "you", -3))
	c.expect(409, "POST", "/household/join", cara, map[string]string{"code": fresh})
	dan := signUp(c, "dan@example.com")
	join(c, dan, fresh)
	if got := members(c, ana); len(got) != 3 || got[2]["hasAccount"] != true {
		t.Fatalf("after Dan joins, Ana sees %v", got)
	}
	c.expect(409, "PUT", "/household/size", ana, map[string]int{"size": 2})
}

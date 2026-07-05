#!/bin/bash
# End-to-end smoke test for the Stór API.
# Flow: signup A → create household (approvals on) → A income → signup B →
# B joins by invite code → B adds pending expense → A approves → approved →
# pots → summary → forecast → soft-delete → IDOR checks.
set -uo pipefail
API=http://localhost:8080/v1
PASS=0; FAIL=0

check() { # name expected actual
  if [[ "$3" == *"$2"* ]]; then PASS=$((PASS+1)); echo "ok   $1";
  else FAIL=$((FAIL+1)); echo "FAIL $1: expected '$2' in: $(echo "$3" | head -c 300)"; fi
}

TS=$(date +%s)
# --- user A signs up ---
A=$(curl -s -X POST $API/auth/signup -d "{\"name\":\"Ross\",\"email\":\"ross+$TS@example.com\",\"password\":\"a strong passphrase 1\"}")
check "signup A" '"accessToken"' "$A"
A_TOKEN=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["accessToken"])')
A_REFRESH=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["refreshToken"])')
A_ID=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin)["user"]["id"])')

# weak password rejected
check "weak password rejected" 'at least 10' "$(curl -s -X POST $API/auth/signup -d '{"name":"x","email":"weak@example.com","password":"short"}')"
# duplicate email rejected
BODY_A="{\"name\":\"Ross\",\"email\":\"ross+$TS@example.com\",\"password\":\"a strong passphrase 1\"}"
DUP=$(curl -s -X POST $API/auth/signup -d "$BODY_A")
check "duplicate email 409" 'email_taken' "$DUP"

# --- refresh rotation + reuse detection ---
R1=$(curl -s -X POST $API/auth/refresh -d "{\"refreshToken\":\"$A_REFRESH\"}")
check "refresh works" '"accessToken"' "$R1"
R2=$(curl -s -X POST $API/auth/refresh -d "{\"refreshToken\":\"$A_REFRESH\"}")
check "refresh reuse rejected" 'unauthorized' "$R2"
NEW_REFRESH=$(echo "$R1" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["refreshToken"])')
R3=$(curl -s -X POST $API/auth/refresh -d "{\"refreshToken\":\"$NEW_REFRESH\"}")
check "family revoked after reuse" 'unauthorized' "$R3"

# --- household ---
HH=$(curl -s -X POST $API/households -H "Authorization: Bearer $A_TOKEN" \
  -d '{"name":"The Murphys","framework":{"needsPercent":50,"wantsPercent":30,"savingsPercent":20},"requiresApprovals":true}')
check "create household" '"inviteCode"' "$HH"
CODE=$(echo "$HH" | python3 -c 'import sys,json;print(json.load(sys.stdin)["inviteCode"])')
check "bad framework rejected" 'sum to 100' "$(curl -s -X POST $API/households -H "Authorization: Bearer $A_TOKEN" -d '{"name":"x","framework":{"needsPercent":50,"wantsPercent":30,"savingsPercent":30},"requiresApprovals":false}')"

curl -s -X PUT $API/me/income -H "Authorization: Bearer $A_TOKEN" -d '{"grossMonthlyMinor":420000,"netMonthlyMinor":300000}' >/dev/null

# --- user B joins ---
B=$(curl -s -X POST $API/auth/signup -d "{\"name\":\"Niámh\",\"email\":\"niamh+$TS@example.com\",\"password\":\"another strong passphrase\"}")
B_TOKEN=$(echo "$B" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["accessToken"])')
B_ID=$(echo "$B" | python3 -c 'import sys,json;print(json.load(sys.stdin)["user"]["id"])')
check "join with bad code 404" 'invalid_code' "$(curl -s -X POST $API/households/join -H "Authorization: Bearer $B_TOKEN" -d '{"inviteCode":"XXXX-XXXX"}')"
J=$(curl -s -X POST $API/households/join -H "Authorization: Bearer $B_TOKEN" -d "{\"inviteCode\":\"$CODE\"}")
check "join household" 'The Murphys' "$J"
curl -s -X PUT $API/me/income -H "Authorization: Bearer $B_TOKEN" -d '{"grossMonthlyMinor":280000,"netMonthlyMinor":200000}' >/dev/null

# --- expense approval workflow ---
E=$(curl -s -X POST $API/household/expenses -H "Authorization: Bearer $B_TOKEN" \
  -d '{"name":"Cinema Club","amountMinor":1999,"frequency":"monthly","category":"entertainment"}')
check "expense created pending" '"pending"' "$E"
E_ID=$(echo "$E" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
AP=$(curl -s -X POST $API/household/expenses/$E_ID/approve -H "Authorization: Bearer $A_TOKEN")
check "approval flips to approved" '"approved"' "$AP"
check "approve non-pending 409" 'not_pending' "$(curl -s -X POST $API/household/expenses/$E_ID/approve -H "Authorization: Bearer $A_TOKEN")"

curl -s -X POST $API/household/expenses -H "Authorization: Bearer $A_TOKEN" -d '{"name":"Rent","amountMinor":120000,"frequency":"monthly","category":"bills"}' >/dev/null
check "bad category rejected" 'unknown category' "$(curl -s -X POST $API/household/expenses -H "Authorization: Bearer $A_TOKEN" -d '{"name":"x","amountMinor":100,"frequency":"monthly","category":"yachts"}')"

# --- pots, summary, forecast ---
P=$(curl -s -X POST $API/household/pots -H "Authorization: Bearer $A_TOKEN" \
  -d '{"name":"House Deposit","currentMinor":420000,"targetMinor":1000000,"monthlyContributionMinor":40000,"emoji":"🏠"}')
P_ID=$(echo "$P" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
check "create pot" 'House Deposit' "$P"

S=$(curl -s $API/household/summary -H "Authorization: Bearer $A_TOKEN")
check "summary income 500000" '"monthlyIncomeMinor":500000' "$S"
# Rent is still pending (approvals on), so only Cinema Club (£19.99) counts.
check "summary counts approved only" '"monthlyExpensesMinor":1999' "$S"
check "summary surplus" '"netSurplusMinor":458001' "$S"

F=$(curl -s "$API/household/forecast?additionalMonthlyExpenseMinor=10000&savingsAdjustmentMinor=5000&potId=$P_ID" -H "Authorization: Bearer $A_TOKEN")
check "forecast time-to-goal 13mo (45k/mo on 580k gap)" '"months":13' "$F"
check "forecast adjusted surplus" '"adjustedNetSurplusMinor":443001' "$F"

# --- soft delete ---
curl -s -X DELETE $API/household/expenses/$E_ID -H "Authorization: Bearer $B_TOKEN" -o /dev/null
L=$(curl -s $API/household/expenses -H "Authorization: Bearer $A_TOKEN")
check "deleted expense gone from list" '!Cinema' "$(echo "$L" | grep -q Cinema && echo Cinema || echo '!Cinema')"

# --- IDOR: outsider C must see nothing of the household ---
C=$(curl -s -X POST $API/auth/signup -d "{\"name\":\"Mallory\",\"email\":\"mallory+$TS@example.com\",\"password\":\"attacker passphrase 99\"}")
C_TOKEN=$(echo "$C" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["accessToken"])')
check "no-household blocked" 'no_household' "$(curl -s $API/household/expenses -H "Authorization: Bearer $C_TOKEN")"
curl -s -X POST $API/households -H "Authorization: Bearer $C_TOKEN" -d '{"name":"Mallory Manor","framework":{"needsPercent":50,"wantsPercent":30,"savingsPercent":20},"requiresApprovals":false}' >/dev/null
check "cross-household expense read 404" 'not_found' "$(curl -s $API/household/expenses/$E_ID -H "Authorization: Bearer $C_TOKEN" -X PATCH -d '{"name":"stolen"}')"
check "cross-household pot delete 404" 'not_found' "$(curl -s -X DELETE $API/household/pots/$P_ID -H "Authorization: Bearer $C_TOKEN")"
check "member cannot patch household" 'forbidden' "$(curl -s -X PATCH $API/household/ -H "Authorization: Bearer $B_TOKEN" -d '{"name":"hacked"}')"
check "no token 401" 'unauthorized' "$(curl -s $API/me)"
check "garbage token 401" 'unauthorized' "$(curl -s $API/me -H 'Authorization: Bearer garbage')"

echo
echo "passed: $PASS  failed: $FAIL"
[[ $FAIL -eq 0 ]]

-- name: CreateUser :one
INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id;

-- name: GetUserByEmail :one
SELECT id, password_hash FROM users WHERE email = $1;

-- name: CreateIdentity :exec
INSERT INTO identities (provider, subject, user_id) VALUES ($1, $2, $3);

-- name: GetUserByIdentity :one
SELECT u.id, u.email FROM identities i JOIN users u ON u.id = i.user_id WHERE i.provider = $1 AND i.subject = $2;

-- name: CreateHousehold :one
INSERT INTO households DEFAULT VALUES RETURNING id;

-- name: CreateMember :exec
INSERT INTO members (household_id, slot, user_id) VALUES ($1, $2, $3);

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: GetSessionUser :one
SELECT u.email, h.setup_complete, coalesce(i.provider, 'password')::text AS method
FROM sessions s
JOIN users u ON u.id = s.user_id
LEFT JOIN identities i ON i.user_id = u.id
JOIN members m ON m.user_id = u.id
JOIN households h ON h.id = m.household_id
WHERE s.token_hash = $1 AND s.expires_at > now();

-- name: GetSessionMember :one
SELECT m.household_id, m.slot, s.user_id,
       (SELECT count(*) FROM members o WHERE o.household_id = m.household_id)::smallint AS size
FROM sessions s
JOIN members m ON m.user_id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now();

-- name: GetMemberByUser :one
SELECT m.household_id, m.slot, h.setup_complete
FROM members m JOIN households h ON h.id = m.household_id
WHERE m.user_id = $1;

-- name: GetTopUpDay :one
SELECT top_up_day FROM households WHERE id = $1;

-- name: ListMembers :many
SELECT slot, contribution_pence, name, net_pay_pence, (user_id IS NOT NULL)::bool AS has_account
FROM members WHERE household_id = $1 ORDER BY slot;

-- name: SetMemberProfile :exec
UPDATE members SET name = $3, net_pay_pence = $4 WHERE household_id = $1 AND slot = $2;

-- name: HasOpenSeat :one
SELECT EXISTS (SELECT 1 FROM members WHERE household_id = $1 AND user_id IS NULL);

-- name: UpsertInvite :exec
INSERT INTO invites (household_id, code_hash, expires_at) VALUES ($1, $2, $3)
ON CONFLICT (household_id) DO UPDATE SET code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at;

-- name: TakeInvite :one
-- Deleting on read makes a code single-use; a failed join rolls the delete back.
DELETE FROM invites WHERE code_hash = $1 AND expires_at > now() RETURNING household_id;

-- name: OpenSeat :one
SELECT slot FROM members WHERE household_id = $1 AND user_id IS NULL ORDER BY slot LIMIT 1 FOR UPDATE;

-- name: ClaimSeat :exec
UPDATE members SET user_id = $3 WHERE household_id = $1 AND slot = $2;

-- name: HouseholdIsDisposable :one
-- Joining discards the joiner's own household, so it must hold nothing and nobody else.
SELECT NOT EXISTS (SELECT 1 FROM members m WHERE m.household_id = $1 AND m.slot <> @slot::smallint AND m.user_id IS NOT NULL)
   AND NOT EXISTS (SELECT 1 FROM transactions t WHERE t.household_id = $1)
   AND NOT EXISTS (SELECT 1 FROM goals g WHERE g.household_id = $1)
   AND NOT EXISTS (SELECT 1 FROM bills b WHERE b.household_id = $1);

-- name: DeleteHousehold :exec
DELETE FROM households WHERE id = $1;

-- name: ListVisibleTransactions :many
-- Server-side privacy: other members' personal ledgers never leave the database.
SELECT id, occurred_at, title, category, amount_pence, actor_slot, actor_name,
       ledger_slot, tag, split_percent, monogram
FROM transactions
WHERE household_id = $1 AND ledger_slot IN (0, @viewer_slot::smallint)
ORDER BY occurred_at DESC, created_at DESC;

-- name: SpentByLedger :many
SELECT ledger_slot, COALESCE(SUM(-amount_pence), 0)::bigint AS spent_pence
FROM transactions
WHERE household_id = $1 AND amount_pence < 0 AND occurred_at >= @since::timestamptz
GROUP BY ledger_slot;

-- name: ListGoals :many
SELECT id, name, saved_pence, target_pence, monthly_pence, note
FROM goals WHERE household_id = $1 ORDER BY name;

-- name: InsertTransaction :exec
INSERT INTO transactions (household_id, id, occurred_at, title, category, amount_pence,
    actor_slot, actor_name, ledger_slot, tag, split_percent, monogram)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (household_id, id) DO NOTHING;

-- name: SetContribution :exec
UPDATE members SET contribution_pence = $3 WHERE household_id = $1 AND slot = $2;

-- name: SetGoalMonthly :execrows
UPDATE goals SET monthly_pence = $3 WHERE household_id = $1 AND id = $2;

-- name: SetTopUpDay :exec
UPDATE households SET top_up_day = $2 WHERE id = $1;

-- name: CompleteSetup :exec
UPDATE households SET setup_complete = true WHERE id = $1;

-- name: LockHousehold :exec
SELECT id FROM households WHERE id = $1 FOR UPDATE;

-- name: MembersBeyondInUse :one
-- A member past the new size can only go if nobody signed in as them and nothing references their slot.
SELECT EXISTS (SELECT 1 FROM members m WHERE m.household_id = $1 AND m.slot > @size::smallint AND m.user_id IS NOT NULL)
    OR EXISTS (SELECT 1 FROM transactions t WHERE t.household_id = $1
               AND (t.ledger_slot > @size::smallint OR t.actor_slot > @size::smallint))
    OR EXISTS (SELECT 1 FROM bills b WHERE b.household_id = $1 AND b.paid_from > @size::smallint);

-- name: DeleteMembersBeyond :exec
DELETE FROM members WHERE household_id = $1 AND slot > @size::smallint;

-- name: AddMembersUpTo :exec
INSERT INTO members (household_id, slot)
SELECT $1, s::smallint FROM generate_series(1, @size::int) s
ON CONFLICT DO NOTHING;

-- name: GetBudgetSettings :one
SELECT essential_pct, discretionary_pct, saving_pct, loan_allocation_pence, cash_cap_pence
FROM households WHERE id = $1;

-- name: SetBudgetSettings :exec
UPDATE households SET essential_pct = $2, discretionary_pct = $3, saving_pct = $4,
    loan_allocation_pence = $5, cash_cap_pence = $6
WHERE id = $1;

-- name: ListBills :many
-- Every member's bills: the plan totals each cash account, and the handler drops other members' rows.
SELECT id, name, group_name, kind, category, amount_pence, frequency, months, paid_from, due_day, meta, is_estimated
FROM bills WHERE household_id = $1 ORDER BY created_at, id;

-- name: UpsertBill :execrows
-- The WHERE stops anyone overwriting another member's personal bill that shares the id.
INSERT INTO bills (household_id, id, name, group_name, kind, category, amount_pence, frequency,
    months, paid_from, due_day, meta, is_estimated)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (household_id, id) DO UPDATE SET
    name = EXCLUDED.name, group_name = EXCLUDED.group_name, kind = EXCLUDED.kind, category = EXCLUDED.category,
    amount_pence = EXCLUDED.amount_pence, frequency = EXCLUDED.frequency, months = EXCLUDED.months,
    paid_from = EXCLUDED.paid_from, due_day = EXCLUDED.due_day, meta = EXCLUDED.meta, is_estimated = EXCLUDED.is_estimated
WHERE bills.paid_from IN (0, @viewer_slot::smallint) AND NOT (bills.paid_from = 0 AND EXCLUDED.paid_from <> 0);

-- name: DeleteBill :execrows
DELETE FROM bills WHERE household_id = $1 AND id = $2 AND paid_from IN (0, @viewer_slot::smallint);

-- name: ListTicks :many
SELECT key FROM transfer_ticks WHERE household_id = $1 AND month = $2;

-- name: TickTransfer :exec
INSERT INTO transfer_ticks (household_id, month, key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: UntickTransfer :exec
DELETE FROM transfer_ticks WHERE household_id = $1 AND month = $2 AND key = $3;

-- name: GetCredentials :one
SELECT u.password_hash, i.provider, i.subject FROM users u LEFT JOIN identities i ON i.user_id = u.id WHERE u.id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: OtherAccountsInHousehold :one
SELECT EXISTS (SELECT 1 FROM members WHERE household_id = $1 AND slot <> @slot::smallint AND user_id IS NOT NULL);

-- name: DeletePersonalBills :exec
DELETE FROM bills WHERE household_id = $1 AND paid_from = @slot::smallint;

-- name: DeletePersonalTransactions :exec
DELETE FROM transactions WHERE household_id = $1 AND ledger_slot = @slot::smallint;

-- name: ForgetActorName :exec
-- Joint spending stays with the household, without the departing member's name on it.
UPDATE transactions SET actor_name = NULL WHERE household_id = $1 AND actor_slot = @slot::smallint;

-- name: ReleaseSeat :exec
UPDATE members SET user_id = NULL, name = '', net_pay_pence = 0, contribution_pence = 0
WHERE household_id = $1 AND slot = @slot::smallint;

-- name: GetBillPaidFrom :one
SELECT paid_from FROM bills WHERE household_id = $1 AND id = $2;

-- name: CountBills :one
SELECT count(*) FROM bills WHERE household_id = $1;

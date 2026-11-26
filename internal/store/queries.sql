-- name: CreateUser :one
INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id;

-- name: GetUserByEmail :one
SELECT id, password_hash FROM users WHERE email = $1;

-- name: CreateHousehold :one
INSERT INTO households DEFAULT VALUES RETURNING id;

-- name: CreateMember :exec
INSERT INTO members (household_id, slot, user_id) VALUES ($1, $2, $3);

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3);

-- name: GetSessionMember :one
SELECT m.household_id, m.slot,
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
SELECT slot, contribution_pence
FROM members WHERE household_id = $1 ORDER BY slot;

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
               AND (t.ledger_slot > @size::smallint OR t.actor_slot > @size::smallint));

-- name: DeleteMembersBeyond :exec
DELETE FROM members WHERE household_id = $1 AND slot > @size::smallint;

-- name: AddMembersUpTo :exec
INSERT INTO members (household_id, slot)
SELECT $1, s::smallint FROM generate_series(1, @size::int) s
ON CONFLICT DO NOTHING;

-- name: CreateHousehold :one
INSERT INTO households (name, needs_percent, wants_percent, savings_percent, requires_approvals, invite_code)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetHouseholdByID :one
SELECT * FROM households WHERE id = $1 AND deleted_at IS NULL;

-- name: GetHouseholdByInviteCode :one
SELECT * FROM households WHERE invite_code = $1 AND deleted_at IS NULL;

-- name: UpdateHousehold :one
UPDATE households
SET name                 = coalesce(sqlc.narg('name'), name),
    needs_percent        = coalesce(sqlc.narg('needs_percent'), needs_percent),
    wants_percent        = coalesce(sqlc.narg('wants_percent'), wants_percent),
    savings_percent      = coalesce(sqlc.narg('savings_percent'), savings_percent),
    requires_approvals   = coalesce(sqlc.narg('requires_approvals'), requires_approvals),
    surplus_split_method = coalesce(sqlc.narg('surplus_split_method'), surplus_split_method)
WHERE id = sqlc.arg('id') AND deleted_at IS NULL
RETURNING *;

-- name: RotateInviteCode :one
UPDATE households SET invite_code = $2 WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: CreateMember :one
INSERT INTO household_members (household_id, user_id, is_creator)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetMembershipByUserID :one
SELECT m.household_id, m.user_id, m.is_creator, h.requires_approvals, h.surplus_split_method,
       h.needs_percent, h.wants_percent, h.savings_percent
FROM household_members m
JOIN households h ON h.id = m.household_id AND h.deleted_at IS NULL
WHERE m.user_id = $1 AND m.deleted_at IS NULL;

-- name: ListMembers :many
SELECT m.user_id, m.is_creator, m.created_at AS joined_at, u.name, u.email, u.age, u.gender
FROM household_members m
JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
WHERE m.household_id = $1 AND m.deleted_at IS NULL
ORDER BY m.created_at;

-- name: CountMembers :one
SELECT count(*) FROM household_members
WHERE household_id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteMember :execrows
UPDATE household_members SET deleted_at = now()
WHERE household_id = $1 AND user_id = $2 AND deleted_at IS NULL AND is_creator = false;

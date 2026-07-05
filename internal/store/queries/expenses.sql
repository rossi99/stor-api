-- name: CreateExpense :one
INSERT INTO expenses (household_id, name, amount_minor, frequency, category, added_by, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetExpense :one
SELECT * FROM expenses
WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL;

-- name: ListExpenses :many
SELECT * FROM expenses
WHERE household_id = $1
  AND deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC;

-- name: UpdateExpense :one
UPDATE expenses
SET name         = coalesce(sqlc.narg('name'), name),
    amount_minor = coalesce(sqlc.narg('amount_minor'), amount_minor),
    frequency    = coalesce(sqlc.narg('frequency'), frequency),
    category     = coalesce(sqlc.narg('category'), category)
WHERE id = sqlc.arg('id') AND household_id = sqlc.arg('household_id') AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteExpense :execrows
UPDATE expenses SET deleted_at = now()
WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL;

-- name: UpsertExpenseApproval :execrows
INSERT INTO expense_approvals (expense_id, user_id)
VALUES ($1, $2)
ON CONFLICT (expense_id, user_id)
DO UPDATE SET deleted_at = NULL
WHERE expense_approvals.deleted_at IS NOT NULL;

-- name: CountExpenseApprovals :one
SELECT count(*) FROM expense_approvals
WHERE expense_id = $1 AND deleted_at IS NULL;

-- name: SetExpenseApproved :exec
UPDATE expenses SET status = 'approved'
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListExpenseApprovalsByHousehold :many
SELECT a.expense_id, a.user_id
FROM expense_approvals a
JOIN expenses e ON e.id = a.expense_id AND e.deleted_at IS NULL
WHERE e.household_id = $1 AND a.deleted_at IS NULL;

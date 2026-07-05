-- name: UpsertIncome :one
INSERT INTO incomes (household_id, user_id, gross_monthly_minor, net_monthly_minor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) WHERE deleted_at IS NULL
DO UPDATE SET gross_monthly_minor = excluded.gross_monthly_minor,
              net_monthly_minor   = excluded.net_monthly_minor
RETURNING *;

-- name: GetIncomeByUserID :one
SELECT * FROM incomes WHERE user_id = $1 AND deleted_at IS NULL;

-- name: ListIncomesByHousehold :many
SELECT i.*, u.name AS member_name
FROM incomes i
JOIN users u ON u.id = i.user_id
WHERE i.household_id = $1 AND i.deleted_at IS NULL
ORDER BY i.created_at;

-- name: CreateSavingsPot :one
INSERT INTO savings_pots (household_id, name, current_minor, target_minor, monthly_contribution_minor, emoji)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetSavingsPot :one
SELECT * FROM savings_pots
WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL;

-- name: ListSavingsPots :many
SELECT * FROM savings_pots
WHERE household_id = $1 AND deleted_at IS NULL
ORDER BY created_at;

-- name: UpdateSavingsPot :one
UPDATE savings_pots
SET name                       = coalesce(sqlc.narg('name'), name),
    current_minor              = coalesce(sqlc.narg('current_minor'), current_minor),
    target_minor               = coalesce(sqlc.narg('target_minor'), target_minor),
    monthly_contribution_minor = coalesce(sqlc.narg('monthly_contribution_minor'), monthly_contribution_minor),
    emoji                      = coalesce(sqlc.narg('emoji'), emoji)
WHERE id = sqlc.arg('id') AND household_id = sqlc.arg('household_id') AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSavingsPot :execrows
UPDATE savings_pots SET deleted_at = now()
WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL;

-- name: UpsertPension :one
INSERT INTO pensions (household_id, user_id, current_value_minor, target_minor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) WHERE deleted_at IS NULL
DO UPDATE SET current_value_minor = excluded.current_value_minor,
              target_minor        = excluded.target_minor
RETURNING *;

-- name: ListPensionsByHousehold :many
SELECT p.*, u.name AS member_name
FROM pensions p
JOIN users u ON u.id = p.user_id
WHERE p.household_id = $1 AND p.deleted_at IS NULL
ORDER BY p.created_at;

-- name: UpsertISA :one
INSERT INTO isas (household_id, user_id, type, balance_minor, contributions_this_year_minor, annual_allowance_minor)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) WHERE deleted_at IS NULL
DO UPDATE SET type                          = excluded.type,
              balance_minor                 = excluded.balance_minor,
              contributions_this_year_minor = excluded.contributions_this_year_minor,
              annual_allowance_minor        = excluded.annual_allowance_minor
RETURNING *;

-- name: ListISAsByHousehold :many
SELECT i.*, u.name AS member_name
FROM isas i
JOIN users u ON u.id = i.user_id
WHERE i.household_id = $1 AND i.deleted_at IS NULL
ORDER BY i.created_at;

-- name: UpsertTaxInfo :one
INSERT INTO tax_info (user_id, gross_monthly_minor, tax_code, tax_system, tax_year,
                      marital_status, pension_contribution_percent, has_student_loan, student_loan_plan)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (user_id)
DO UPDATE SET gross_monthly_minor          = excluded.gross_monthly_minor,
              tax_code                     = excluded.tax_code,
              tax_system                   = excluded.tax_system,
              tax_year                     = excluded.tax_year,
              marital_status               = excluded.marital_status,
              pension_contribution_percent = excluded.pension_contribution_percent,
              has_student_loan             = excluded.has_student_loan,
              student_loan_plan            = excluded.student_loan_plan,
              deleted_at                   = NULL
RETURNING *;

-- name: GetTaxInfo :one
SELECT * FROM tax_info WHERE user_id = $1 AND deleted_at IS NULL;

-- name: UpsertMonthlySnapshot :exec
INSERT INTO monthly_snapshots (household_id, month, total_expenses_minor)
VALUES ($1, $2, $3)
ON CONFLICT (household_id, month)
DO UPDATE SET total_expenses_minor = excluded.total_expenses_minor;

-- name: GetMonthlySnapshot :one
SELECT * FROM monthly_snapshots
WHERE household_id = $1 AND month = $2 AND deleted_at IS NULL;

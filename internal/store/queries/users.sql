-- name: CreateUser :one
INSERT INTO users (email, password_hash, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateUserProfile :one
UPDATE users
SET name     = coalesce(sqlc.narg('name'), name),
    age      = coalesce(sqlc.narg('age'), age),
    gender   = coalesce(sqlc.narg('gender'), gender),
    currency = coalesce(sqlc.narg('currency'), currency)
WHERE id = sqlc.arg('id') AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1 AND deleted_at IS NULL;

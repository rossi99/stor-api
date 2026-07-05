# Stór API

Go backend for the Stór household-finance app. Chi + PostgreSQL + sqlc; auth is
self-owned (argon2id passwords, short-lived HS256 access JWTs, rotating opaque
refresh tokens with reuse detection).

## Dev environment

```sh
tilt up          # Postgres (docker compose) → migrations → API with live reload
```

or piecemeal via [Task](https://taskfile.dev):

```sh
task db-up       # start Postgres
task migrate     # apply migrations
task run         # run the API on :8080
task test        # unit tests
task sqlc        # regenerate internal/store from SQL
scripts/smoke.sh # end-to-end flow against a running server
```

Config is env-only — see `.env.example`. `JWT_SIGNING_KEY` must be ≥32 bytes or
the server refuses to boot.

## Conventions

- Base path `/v1`; errors are `{"error":{"code","message"}}`.
- Money is integer minor units (pence): `amountMinor` + `"currency":"GBP"`.
- Timestamps are RFC 3339 Nano UTC.
- Every table has `created_at`/`updated_at`/`deleted_at`; deletes are soft, and
  reads filter `deleted_at IS NULL`.
- Authorization: middleware resolves the caller's household membership per
  request; every query is scoped by `household_id`/`user_id` server-side.

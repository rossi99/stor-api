package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stor.app/api/internal/config"
	mailer "stor.app/api/internal/mail"
	"stor.app/api/internal/store"
)

type Handler struct {
	Cfg   config.Config
	Store *store.Queries
	Pool  *pgxpool.Pool
	Mail  mailer.Mailer
	Log   *slog.Logger
}

// withTx runs fn against a transactional copy of the store, committing when it
// returns nil. Used everywhere a state change spans multiple rows (approvals,
// signup token issuance, joins).
func (h *Handler) withTx(ctx context.Context, fn func(q *store.Queries) error) error {
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(h.Store.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// audit records security-relevant events as structured logs, tied to the
// request via chi's request ID.
func (h *Handler) audit(ctx context.Context, event string, args ...any) {
	args = append(args, "audit", true, "request_id", chimw.GetReqID(ctx))
	h.Log.Info(event, args...)
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) > 254 {
		return "", fmt.Errorf("email too long")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", fmt.Errorf("invalid email address")
	}
	return email, nil
}

func validName(name string) bool {
	n := len(strings.TrimSpace(name))
	return n >= 1 && n <= 100
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// isUniqueViolation reports whether err is a Postgres 23505 (unique index
// conflict) — used for duplicate emails and invite-code collisions.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

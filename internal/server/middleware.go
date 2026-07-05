package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"stor.app/api/internal/auth"
	"stor.app/api/internal/domain"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

// requireAuth verifies the bearer token and puts the user ID in context.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			httpx.Unauthorized(w)
			return
		}
		userID, err := auth.VerifyAccessToken(s.cfg.JWTSigningKey, token)
		if err != nil {
			httpx.Unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(reqctx.WithUserID(r.Context(), userID)))
	})
}

// requireHousehold resolves the caller's household membership from the DB on
// every request — the single authorization gate for all household-scoped
// routes. Handlers below it only ever see IDs from this membership, never from
// the client, which is what prevents cross-household access.
func (s *Server) requireHousehold(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := reqctx.UserID(r.Context())
		if !ok {
			httpx.Unauthorized(w)
			return
		}
		row, err := s.store.GetMembershipByUserID(r.Context(), userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusPreconditionRequired, "no_household", "join or create a household first")
				return
			}
			s.log.Error("resolve membership", "error", err)
			httpx.Internal(w)
			return
		}
		m := membershipFromRow(row)
		next.ServeHTTP(w, r.WithContext(reqctx.WithMembership(r.Context(), m)))
	})
}

func membershipFromRow(row store.GetMembershipByUserIDRow) reqctx.Membership {
	return reqctx.Membership{
		HouseholdID:       row.HouseholdID,
		UserID:            row.UserID,
		IsCreator:         row.IsCreator,
		RequiresApprovals: row.RequiresApprovals,
		SplitMethod:       splitMethod(row.SurplusSplitMethod),
		NeedsPercent:      row.NeedsPercent,
		WantsPercent:      row.WantsPercent,
		SavingsPercent:    row.SavingsPercent,
	}
}

// securityHeaders sets defense-in-depth headers. The API serves JSON only, so
// the CSP and sniffing rules exist to blunt any response being coerced into a
// browser context.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func splitMethod(s string) domain.SplitMethod {
	if m := domain.SplitMethod(s); m.Valid() {
		return m
	}
	return domain.SplitProportional
}

// requestLogger emits one structured line per request with the request ID
// assigned by chi middleware. Paths never contain secrets by design (tokens
// travel in headers/bodies only).
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", chimw.GetReqID(r.Context()),
			)
		})
	}
}

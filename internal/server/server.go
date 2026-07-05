package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"stor.app/api/internal/config"
	"stor.app/api/internal/handlers"
	"stor.app/api/internal/mail"
	"stor.app/api/internal/store"
)

type Server struct {
	cfg   config.Config
	store *store.Queries
	log   *slog.Logger
	http.Handler
}

func New(cfg config.Config, st *store.Queries, pool *pgxpool.Pool, mailer mail.Mailer, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, log: log}

	h := &handlers.Handler{Cfg: cfg, Store: st, Pool: pool, Mail: mailer, Log: log}

	authLimiter := newIPRateLimiter(10) // 10/min: signin, signup, refresh, reset
	joinLimiter := newIPRateLimiter(10) // invite-code guessing

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(requestLogger(log))
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(securityHeaders)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Use(authLimiter.middleware)
			r.Post("/signup", h.Signup)
			r.Post("/signin", h.Signin)
			r.Post("/refresh", h.Refresh)
			r.Post("/forgot-password", h.ForgotPassword)
			r.Post("/reset-password", h.ResetPassword)
			r.With(s.requireAuth).Post("/signout", h.Signout)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Get("/me", h.GetMe)
			r.Patch("/me", h.PatchMe)
			r.Get("/me/tax-info", h.GetTaxInfo)
			r.Put("/me/tax-info", h.PutTaxInfo)

			r.Post("/households", h.CreateHousehold)
			r.With(joinLimiter.middleware).Post("/households/join", h.JoinHousehold)

			r.Route("/household", func(r chi.Router) {
				r.Use(s.requireHousehold)

				r.Get("/", h.GetHousehold)
				r.Patch("/", h.PatchHousehold)
				r.Post("/invite-code/rotate", h.RotateInviteCode)
				r.Get("/members", h.ListMembers)
				r.Delete("/members/{userID}", h.RemoveMember)

				r.Get("/expenses", h.ListExpenses)
				r.Post("/expenses", h.CreateExpense)
				r.Patch("/expenses/{expenseID}", h.UpdateExpense)
				r.Delete("/expenses/{expenseID}", h.DeleteExpense)
				r.Post("/expenses/{expenseID}/approve", h.ApproveExpense)

				r.Get("/pots", h.ListPots)
				r.Post("/pots", h.CreatePot)
				r.Patch("/pots/{potID}", h.UpdatePot)
				r.Delete("/pots/{potID}", h.DeletePot)

				r.Get("/incomes", h.ListIncomes)
				r.Get("/pensions", h.ListPensions)
				r.Get("/isas", h.ListISAs)

				r.Get("/summary", h.Summary)
				r.Get("/forecast", h.Forecast)
			})

			// Personal finance records live under /me but need household
			// context (they're shown household-wide).
			r.Group(func(r chi.Router) {
				r.Use(s.requireHousehold)
				r.Get("/me/income", h.GetMyIncome)
				r.Put("/me/income", h.PutMyIncome)
				r.Put("/me/pension", h.PutMyPension)
				r.Put("/me/isa", h.PutMyISA)
			})
		})
	})

	s.Handler = r
	return s
}

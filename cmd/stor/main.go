package main

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rossi99/stor/internal/api"
	"rossi99/stor/internal/store"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("database config", "err", err)
		os.Exit(1)
	}
	if err := store.Migrate(ctx, pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	addr := ":" + cmp.Or(os.Getenv("PORT"), "8080")
	srv := &http.Server{Addr: addr, Handler: api.New(pool), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

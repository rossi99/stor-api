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
	cfg := api.Config{
		GoogleClientID: os.Getenv("GOOGLE_CLIENT_ID"),
		AppleBundleID:  os.Getenv("APPLE_BUNDLE_ID"),
		AppleTeamID:    os.Getenv("APPLE_TEAM_ID"),
		AppleKeyID:     os.Getenv("APPLE_KEY_ID"),
	}
	if pemText := os.Getenv("APPLE_PRIVATE_KEY"); pemText != "" {
		if cfg.AppleBundleID == "" || cfg.AppleTeamID == "" || cfg.AppleKeyID == "" {
			slog.Error("APPLE_PRIVATE_KEY needs APPLE_BUNDLE_ID, APPLE_TEAM_ID and APPLE_KEY_ID")
			os.Exit(1)
		}
		if cfg.AppleKey, err = api.ParseAppleKey(pemText); err != nil {
			slog.Error("apple key", "err", err)
			os.Exit(1)
		}
	}
	addr := ":" + cmp.Or(os.Getenv("PORT"), "8080")
	srv := &http.Server{Addr: addr, Handler: api.New(pool, cfg), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

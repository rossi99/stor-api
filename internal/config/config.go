package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL     string
	JWTSigningKey   []byte
	Port            int
	Env             string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	ResetTokenTTL   time.Duration
}

func (c Config) IsDevelopment() bool { return c.Env == "development" }

// Load reads configuration from the environment and fails fast on anything
// unsafe or missing so a misconfigured server never starts.
func Load() (Config, error) {
	cfg := Config{
		Env:             getenv("ENV", "production"),
		Port:            8080,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		ResetTokenTTL:   30 * time.Minute,
	}

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}

	key := os.Getenv("JWT_SIGNING_KEY")
	if len(key) < 32 {
		return cfg, fmt.Errorf("JWT_SIGNING_KEY must be at least 32 bytes, got %d", len(key))
	}
	cfg.JWTSigningKey = []byte(key)

	if p := os.Getenv("PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return cfg, fmt.Errorf("PORT must be a valid port number, got %q", p)
		}
		cfg.Port = port
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

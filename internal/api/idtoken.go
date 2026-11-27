package api

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

var errInvalidToken = errors.New("invalid identity token")

// verifier checks the RS256 ID tokens Apple and Google sign, against their published keys.
type verifier struct {
	keysURL  string
	issuers  []string
	audience string
	client   *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
	tried   time.Time
}

type identity struct {
	subject, email string
	emailVerified  bool
	issuedAt       time.Time
}

func (v *verifier) verify(ctx context.Context, token string) (identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identity{}, errInvalidToken
	}
	var header struct{ Alg, Kid string }
	if err := decodeSegment(parts[0], &header); err != nil || header.Alg != "RS256" {
		return identity{}, errInvalidToken
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return identity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return identity{}, errInvalidToken
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return identity{}, errInvalidToken
	}
	var claims struct {
		Iss, Aud, Sub, Email string
		// Google sends a bool, Apple sometimes the string "true".
		EmailVerified json.RawMessage `json:"email_verified"`
		Exp, Iat      int64
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return identity{}, errInvalidToken
	}
	const skew = time.Minute
	if !slices.Contains(v.issuers, claims.Iss) || claims.Aud != v.audience || claims.Sub == "" ||
		time.Now().After(time.Unix(claims.Exp, 0).Add(skew)) {
		return identity{}, errInvalidToken
	}
	verified := string(claims.EmailVerified)
	return identity{
		subject:       claims.Sub,
		email:         strings.ToLower(claims.Email),
		emailVerified: verified == "true" || verified == `"true"`,
		issuedAt:      time.Unix(claims.Iat, 0),
	}, nil
}

// key refetches the key set hourly, or on an unknown kid, at most once a minute; a failed refresh keeps the old keys.
func (v *verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.keys[kid]; ok && time.Since(v.fetched) < time.Hour {
		return key, nil
	}
	if time.Since(v.tried) > time.Minute {
		v.tried = time.Now()
		if err := v.fetch(context.WithoutCancel(ctx)); err != nil {
			if v.keys == nil {
				return nil, err
			}
			slog.Warn("signing key refresh failed, keeping cached keys", "url", v.keysURL, "err", err)
		}
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, errInvalidToken
}

func (v *verifier) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.keysURL, nil)
	if err != nil {
		return fmt.Errorf("fetching signing keys: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching signing keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching signing keys: %s", resp.Status)
	}
	var set struct {
		Keys []struct{ Kid, Kty, N, E string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return fmt.Errorf("decoding signing keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || errN != nil || errE != nil || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	v.keys, v.fetched = keys, time.Now()
	return nil
}

func decodeSegment(segment string, dst any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

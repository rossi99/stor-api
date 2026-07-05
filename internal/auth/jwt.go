package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const issuer = "stor-api"

// MintAccessToken issues a short-lived HS256 JWT. Deliberately minimal claims:
// no household or role — those are resolved per request so revocations and
// member removals take effect immediately.
func MintAccessToken(key []byte, userID uuid.UUID, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		ID:        uuid.NewString(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}

// VerifyAccessToken validates signature, algorithm, issuer and expiry, and
// returns the authenticated user ID.
func VerifyAccessToken(key []byte, token string) (uuid.UUID, error) {
	parsed, err := jwt.ParseWithClaims(token, &jwt.RegisteredClaims{}, func(t *jwt.Token) (any, error) {
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return uuid.Nil, err
	}
	claims, ok := parsed.Claims.(*jwt.RegisteredClaims)
	if !ok || claims.Subject == "" {
		return uuid.Nil, fmt.Errorf("missing subject")
	}
	return uuid.Parse(claims.Subject)
}

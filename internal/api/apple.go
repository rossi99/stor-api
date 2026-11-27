package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const appleURL = "https://appleid.apple.com"

// ParseAppleKey reads the .p8 Sign in with Apple key from Apple's developer portal.
func ParseAppleKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("apple key: no PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing apple key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple key is not an EC key")
	}
	return ec, nil
}

// appleRevoker revokes an account's Sign in with Apple grant when it is deleted, as App Store review requires.
type appleRevoker struct {
	baseURL, clientID, teamID, keyID string
	key                              *ecdsa.PrivateKey
	client                           *http.Client
}

// revoke trades a fresh authorization code for a refresh token, then revokes it.
func (a *appleRevoker) revoke(ctx context.Context, code string) error {
	secret, err := a.clientSecret(time.Now())
	if err != nil {
		return err
	}
	var tokens struct {
		RefreshToken string `json:"refresh_token"`
	}
	form := url.Values{"client_id": {a.clientID}, "client_secret": {secret}, "grant_type": {"authorization_code"}, "code": {code}}
	if err := a.post(ctx, "/auth/token", form, &tokens); err != nil {
		return fmt.Errorf("exchanging apple code: %w", err)
	}
	form = url.Values{"client_id": {a.clientID}, "client_secret": {secret}, "token": {tokens.RefreshToken}, "token_type_hint": {"refresh_token"}}
	if err := a.post(ctx, "/auth/revoke", form, nil); err != nil {
		return fmt.Errorf("revoking apple token: %w", err)
	}
	return nil
}

func (a *appleRevoker) post(ctx context.Context, path string, form url.Values, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apple returned %s", resp.Status)
	}
	if dst == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// clientSecret is the short-lived ES256 JWT Apple takes in place of a static secret.
func (a *appleRevoker) clientSecret(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": a.keyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": a.teamID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "aud": appleURL, "sub": a.clientID})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing apple client secret: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

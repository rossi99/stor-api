package api

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeProvider serves a signing key set and Apple's token and revoke endpoints.
type fakeProvider struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	key     *rsa.PrivateKey
	kid     string
	revoked []string
	// down makes the key set endpoint fail, as during a provider outage.
	down bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{t: t}
	f.rotate("k1")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": f.kid, "kty": "RSA", "n": b64(f.key.N.Bytes()), "e": b64(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_secret") == "" || r.FormValue("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"refresh_token": "refresh-for-" + r.FormValue("code")})
	})
	mux.HandleFunc("POST /auth/revoke", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revoked = append(f.revoked, r.FormValue("token"))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) rotate(kid string) {
	f.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key, f.kid = key, kid
}

func (f *fakeProvider) token(claims map[string]any) string {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": f.kid})
	body, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func (f *fakeProvider) verifier() *verifier {
	return &verifier{keysURL: f.srv.URL + "/auth/keys", issuers: []string{"https://accounts.google.com"}, audience: "google-client", client: f.srv.Client()}
}

func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func googleClaims(sub, email string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": "https://accounts.google.com", "aud": "google-client", "sub": sub, "email": email,
		"email_verified": true, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
}

func appleClaims(sub, email string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": "https://appleid.apple.com", "aud": "com.stor.app", "sub": sub, "email": email,
		"email_verified": "true", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
}

func TestVerifier(t *testing.T) {
	f := newFakeProvider(t)
	other := newFakeProvider(t)
	unsigned := b64([]byte(`{"alg":"none","kid":"k1"}`)) + "." + b64([]byte(`{"iss":"https://accounts.google.com","aud":"google-client","sub":"g-1","exp":9999999999}`)) + "."
	tests := []struct {
		name  string
		token string
		want  identity
	}{
		{"accepts a valid token and lower-cases the email", f.token(googleClaims("g-1", "Ana@Example.com")),
			identity{subject: "g-1", email: "ana@example.com", emailVerified: true}},
		{"reads apple's string email_verified", f.token(with(googleClaims("g-1", "a@x.com"), "email_verified", "true")),
			identity{subject: "g-1", email: "a@x.com", emailVerified: true}},
		{"reports an unverified email", f.token(with(googleClaims("g-1", "a@x.com"), "email_verified", false)),
			identity{subject: "g-1", email: "a@x.com"}},
		{"rejects another audience", f.token(with(googleClaims("g-1", "a@x.com"), "aud", "someone-else")), identity{}},
		{"rejects another issuer", f.token(with(googleClaims("g-1", "a@x.com"), "iss", "https://evil.example")), identity{}},
		{"rejects an expired token", f.token(with(googleClaims("g-1", "a@x.com"), "exp", time.Now().Add(-2*time.Minute).Unix())), identity{}},
		{"rejects a token without a subject", f.token(googleClaims("", "a@x.com")), identity{}},
		{"rejects a token signed by another key", other.token(googleClaims("g-1", "a@x.com")), identity{}},
		{"rejects an unsigned token", unsigned, identity{}},
		{"rejects garbage", "not.a.token", identity{}},
	}
	v := f.verifier()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.verify(t.Context(), tt.token)
			if tt.want.subject == "" {
				if !errors.Is(err, errInvalidToken) {
					t.Fatalf("err = %v, want errInvalidToken", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got.issuedAt = time.Time{}
			if got != tt.want {
				t.Fatalf("identity = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVerifierRefetchesRotatedKeysAtMostOnceAMinute(t *testing.T) {
	f := newFakeProvider(t)
	v := f.verifier()
	if _, err := v.verify(t.Context(), f.token(googleClaims("g-1", "a@x.com"))); err != nil {
		t.Fatal(err)
	}
	f.rotate("k2")
	rotated := f.token(googleClaims("g-1", "a@x.com"))
	if _, err := v.verify(t.Context(), rotated); !errors.Is(err, errInvalidToken) {
		t.Fatalf("refetched within a minute: err = %v", err)
	}
	v.tried = v.tried.Add(-2 * time.Minute)
	if _, err := v.verify(t.Context(), rotated); err != nil {
		t.Fatalf("rotated key not picked up: %v", err)
	}
}

func TestVerifierKeepsCachedKeysWhenRefreshFails(t *testing.T) {
	f := newFakeProvider(t)
	v := f.verifier()
	token := f.token(googleClaims("g-1", "a@x.com"))
	if _, err := v.verify(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	v.fetched = v.fetched.Add(-2 * time.Hour)
	v.tried = v.tried.Add(-2 * time.Minute)
	if _, err := v.verify(t.Context(), token); err != nil {
		t.Fatalf("failed refresh dropped the cached keys: %v", err)
	}
}

func TestAppleClientSecretIsSignedByTheKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &appleRevoker{clientID: "com.stor.app", teamID: "TEAM", keyID: "KEY", key: key}
	secret, err := a.clientSecret(time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(secret, ".")
	var header map[string]string
	var claims map[string]any
	if len(parts) != 3 || decodeSegment(parts[0], &header) != nil || decodeSegment(parts[1], &claims) != nil {
		t.Fatalf("malformed secret %q", secret)
	}
	if header["alg"] != "ES256" || header["kid"] != "KEY" || claims["iss"] != "TEAM" || claims["sub"] != "com.stor.app" ||
		claims["aud"] != "https://appleid.apple.com" || claims["exp"] != 1_800_000_300.0 {
		t.Fatalf("header %v claims %v", header, claims)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature %x: %v", sig, err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
}

func TestParseAppleKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAppleKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	if err != nil || !got.Equal(key) {
		t.Fatalf("ParseAppleKey = %v, %v", got, err)
	}
	if _, err := ParseAppleKey("not a key"); err == nil {
		t.Fatal("accepted text with no PEM block")
	}
}

func setupProviders(t *testing.T) (client, *pgxpool.Pool, *fakeProvider) {
	f := newFakeProvider(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, pool := setupWith(t, Config{GoogleClientID: "google-client", AppleBundleID: "com.stor.app", AppleTeamID: "TEAM", AppleKeyID: "KEY",
		AppleKey: key, googleKeysURL: f.srv.URL + "/auth/keys", appleURL: f.srv.URL})
	return c, pool, f
}

func provider(name, token string) map[string]string {
	return map[string]string{"provider": name, "idToken": token}
}

func TestProviderSignIn(t *testing.T) {
	c, _, f := setupProviders(t)
	ana := f.token(googleClaims("g-1", "Ana@Example.com"))

	first := c.expect(201, "POST", "/auth/provider", "", provider("google", ana))
	if first["email"] != "ana@example.com" || first["method"] != "google" || first["token"] == "" {
		t.Fatalf("first sign-in = %v", first)
	}
	token := first["token"].(string)
	c.expect(200, "GET", "/household", token, nil)
	c.expect(204, "POST", "/household/setup-complete", token, nil)

	again := c.expect(200, "POST", "/auth/provider", "", provider("google", ana))
	if again["email"] != "ana@example.com" || again["method"] != "google" || again["hasCompletedSetup"] != true || again["token"] == token {
		t.Fatalf("returning sign-in = %v", again)
	}
	if restored := c.expect(200, "GET", "/auth/session", token, nil); restored["method"] != "google" {
		t.Fatalf("restored session = %v", restored)
	}

	c.expect(401, "POST", "/auth/sign-in", "", map[string]string{"email": "ana@example.com", "password": ""})
	c.expect(409, "POST", "/auth/sign-up", "", map[string]string{"email": "ana@example.com", "password": "password-1"})
	c.expect(409, "POST", "/auth/provider", "", provider("apple", f.token(appleClaims("a-1", "ana@example.com"))))
	signUp(c, "sam@example.com")
	c.expect(409, "POST", "/auth/provider", "", provider("google", f.token(googleClaims("g-2", "sam@example.com"))))

	c.expect(400, "POST", "/auth/provider", "", provider("google", f.token(with(googleClaims("g-3", "new@example.com"), "email_verified", false))))
	c.expect(400, "POST", "/auth/provider", "", provider("google", f.token(googleClaims("g-3", ""))))
	c.expect(400, "POST", "/auth/provider", "", provider("facebook", ana))
	c.expect(401, "POST", "/auth/provider", "", provider("apple", ana))
	c.expect(401, "POST", "/auth/provider", "", provider("google", "not.a.token"))
}

func TestDeleteProviderAccount(t *testing.T) {
	c, pool, f := setupProviders(t)
	token := c.expect(201, "POST", "/auth/provider", "", provider("apple", f.token(appleClaims("a-1", "x1@privaterelay.appleid.com"))))["token"].(string)

	stale := with(appleClaims("a-1", "x1@privaterelay.appleid.com"), "iat", time.Now().Add(-10*time.Minute).Unix())
	c.expect(401, "DELETE", "/auth/account", token, map[string]string{"idToken": f.token(stale)})
	c.expect(401, "DELETE", "/auth/account", token, map[string]string{"idToken": f.token(appleClaims("a-2", "x2@privaterelay.appleid.com"))})
	c.expect(401, "DELETE", "/auth/account", token, map[string]string{"password": "password-1"})

	fresh := f.token(appleClaims("a-1", "x1@privaterelay.appleid.com"))
	c.expect(204, "DELETE", "/auth/account", token, map[string]string{"idToken": fresh, "authorizationCode": "code-1"})
	c.expect(401, "GET", "/household", token, nil)
	var users int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("users left = %d (%v)", users, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revoked) != 1 || f.revoked[0] != "refresh-for-code-1" {
		t.Fatalf("revoked = %v", f.revoked)
	}
}

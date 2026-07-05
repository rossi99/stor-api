package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var testKey = []byte("test-signing-key-at-least-32-bytes!!")

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("expected argon2id hash, got %q", hash[:20])
	}
	if ok, _ := VerifyPassword("correct horse battery staple", hash); !ok {
		t.Error("correct password rejected")
	}
	if ok, _ := VerifyPassword("wrong password", hash); ok {
		t.Error("wrong password accepted")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Error("short password accepted")
	}
	if err := ValidatePassword("password123"); err == nil {
		t.Error("common password accepted")
	}
	if err := ValidatePassword("a perfectly fine passphrase"); err != nil {
		t.Errorf("good password rejected: %v", err)
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	userID := uuid.New()
	token, err := MintAccessToken(testKey, userID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyAccessToken(testKey, token)
	if err != nil {
		t.Fatal(err)
	}
	if got != userID {
		t.Errorf("subject = %v, want %v", got, userID)
	}
}

func TestAccessTokenRejections(t *testing.T) {
	token, _ := MintAccessToken(testKey, uuid.New(), time.Minute)

	if _, err := VerifyAccessToken([]byte("a-different-32-byte-signing-key!!!"), token); err == nil {
		t.Error("token verified with wrong key")
	}
	expired, _ := MintAccessToken(testKey, uuid.New(), -time.Minute)
	if _, err := VerifyAccessToken(testKey, expired); err == nil {
		t.Error("expired token verified")
	}
	// alg=none style tampering: strip the signature.
	parts := strings.Split(token, ".")
	if _, err := VerifyAccessToken(testKey, parts[0]+"."+parts[1]+"."); err == nil {
		t.Error("unsigned token verified")
	}
}

func TestOpaqueTokenHashing(t *testing.T) {
	token, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 {
		t.Errorf("hash length = %d, want 32", len(hash))
	}
	if string(HashToken(token)) != string(hash) {
		t.Error("HashToken does not reproduce stored hash")
	}
	token2, _, _ := NewOpaqueToken()
	if token == token2 {
		t.Error("two tokens identical")
	}
}

func TestNewInviteCode(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		code, err := NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 9 || code[4] != '-' {
			t.Fatalf("bad format: %q", code)
		}
		for _, c := range code[:4] + code[5:] {
			if !strings.ContainsRune(inviteAlphabet, c) {
				t.Fatalf("char %q outside alphabet", c)
			}
		}
		seen[code] = true
	}
	if len(seen) < 100 {
		t.Error("duplicate invite codes in 100 draws")
	}
}

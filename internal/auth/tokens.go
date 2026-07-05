package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
)

// NewOpaqueToken returns a 256-bit random token (sent to the client) and the
// SHA-256 hash under which it is stored — the raw value never touches the DB.
func NewOpaqueToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Invite codes: 8 chars from an unambiguous alphabet (no 0/O/1/I/L), ~40 bits
// of entropy, formatted XXXX-XXXX to match the app's display style.
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func NewInviteCode() (string, error) {
	buf := make([]byte, 8)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(inviteAlphabet))))
		if err != nil {
			return "", err
		}
		buf[i] = inviteAlphabet[n.Int64()]
	}
	return string(buf[:4]) + "-" + string(buf[4:]), nil
}

package auth

import (
	"fmt"
	"strings"

	"github.com/alexedwards/argon2id"
)

// OWASP-recommended argon2id parameters: 64 MiB memory, 3 iterations.
var hashParams = &argon2id.Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

const MinPasswordLength = 10

// A tiny denylist of the most common breached passwords long enough to pass
// the length check. A full HIBP integration can replace this later.
var commonPasswords = map[string]struct{}{
	"1234567890": {}, "qwertyuiop": {}, "password123": {}, "password1234": {},
	"1q2w3e4r5t": {}, "iloveyou123": {}, "0987654321": {}, "1234qwerty": {},
	"asdfghjkl;": {}, "qwerty123456": {},
}

func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(pw) > 256 {
		return fmt.Errorf("password must be at most 256 characters")
	}
	if _, bad := commonPasswords[strings.ToLower(pw)]; bad {
		return fmt.Errorf("password is too common")
	}
	return nil
}

func HashPassword(pw string) (string, error) {
	return argon2id.CreateHash(pw, hashParams)
}

func VerifyPassword(pw, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(pw, hash)
}

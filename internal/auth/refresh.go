package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// refreshTokenBytes is the entropy of a refresh token: 256 bits (security.md §2).
const refreshTokenBytes = 32

// newRefreshToken returns an opaque refresh token and the SHA-256 hash that the database stores instead.
func newRefreshToken() (token string, hash []byte) {
	raw := make([]byte, refreshTokenBytes)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(raw)
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:]
}

// hashRefreshToken returns the stored hash of a presented token. It reports false for a string that no call to
// newRefreshToken could have produced, so such a token never reaches the database.
func hashRefreshToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != refreshTokenBytes {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], true
}

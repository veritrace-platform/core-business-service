// Package auth signs users in and out, issues and verifies access tokens, and rotates refresh tokens
// (ADR-0007). It also serves the signed-in user's own account.
package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
)

// AccessTokenTTL is the lifetime of an access token (ADR-0007).
const AccessTokenTTL = 15 * time.Minute

// Access token verification failures.
var (
	ErrTokenExpired = errors.New("access token expired")
	ErrTokenInvalid = errors.New("access token invalid")
)

var kidPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type signingKey struct {
	id      string
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

// KeySet holds the Ed25519 keys of JWT_SIGNING_KEYS. The first key signs new tokens; every key verifies, so
// tokens signed before a rotation stay valid until they expire.
type KeySet struct {
	keys []signingKey
}

// ParseKeySet reads a comma-separated list of "<kid>:<base64 of a 32-byte Ed25519 seed>" entries, for example
// "2026-09:<output of openssl rand -base64 32>".
func ParseKeySet(spec string) (*KeySet, error) {
	set := &KeySet{}
	seen := map[string]bool{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, encoded, ok := strings.Cut(entry, ":")
		if !ok || !kidPattern.MatchString(kid) {
			return nil, fmt.Errorf("signing key %d: want <kid>:<base64 seed> with a kid of 1-64 characters from A-Z, a-z, 0-9, '.', '_', '-'", len(set.keys)+1)
		}
		if seen[kid] {
			return nil, fmt.Errorf("signing key %q is listed twice", kid)
		}
		seed, err := decodeSeed(encoded)
		if err != nil {
			return nil, fmt.Errorf("signing key %q: %w", kid, err)
		}
		private := ed25519.NewKeyFromSeed(seed)
		set.keys = append(set.keys, signingKey{id: kid, private: private, public: private.Public().(ed25519.PublicKey)})
		seen[kid] = true
	}
	if len(set.keys) == 0 {
		return nil, errors.New("no signing key configured")
	}
	return set, nil
}

// decodeSeed accepts standard or URL-safe base64, with or without padding.
func decodeSeed(encoded string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if seed, err := enc.DecodeString(encoded); err == nil {
			if len(seed) != ed25519.SeedSize {
				return nil, fmt.Errorf("seed has %d bytes, want %d", len(seed), ed25519.SeedSize)
			}
			return seed, nil
		}
	}
	return nil, errors.New("seed is not base64")
}

// JWK is a public key in JSON Web Key form (RFC 8037).
type JWK struct {
	KeyType   string `json:"kty"`
	Curve     string `json:"crv"`
	KeyID     string `json:"kid"`
	X         string `json:"x"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
}

// JWKS is the document served at /.well-known/jwks.json.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns the public keys of the set.
func (s *KeySet) JWKS() JWKS {
	doc := JWKS{Keys: make([]JWK, 0, len(s.keys))}
	for _, k := range s.keys {
		doc.Keys = append(doc.Keys, JWK{
			KeyType: "OKP", Curve: "Ed25519", KeyID: k.id, Algorithm: "EdDSA", Use: "sig",
			X: base64.RawURLEncoding.EncodeToString(k.public),
		})
	}
	return doc
}

func (s *KeySet) public(kid string) (ed25519.PublicKey, bool) {
	for _, k := range s.keys {
		if k.id == kid {
			return k.public, true
		}
	}
	return nil, false
}

// claims are the access token claims of ADR-0007: sub, tid, role, iat, exp, and jti.
type claims struct {
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Tokens issues and verifies access tokens.
type Tokens struct {
	keys *KeySet
	now  func() time.Time
}

// NewTokens returns Tokens that sign with keys and read the time from now.
func NewTokens(keys *KeySet, now func() time.Time) *Tokens {
	return &Tokens{keys: keys, now: now}
}

// Issue signs an access token for p. The token's jti is p.SessionID, the refresh session that issued it.
func (t *Tokens) Issue(p identity.Principal) (token string, expiresAt time.Time, err error) {
	issuedAt := t.now().UTC().Truncate(time.Second)
	expiresAt = issuedAt.Add(AccessTokenTTL)
	signer := t.keys.keys[0]
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		TenantID: p.TenantID.String(),
		Role:     string(p.Role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   p.UserID.String(),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        p.SessionID.String(),
		},
	})
	jwtToken.Header["kid"] = signer.id
	token, err = jwtToken.SignedString(signer.private)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return token, expiresAt, nil
}

// Verify checks an access token and returns its principal. It returns ErrTokenExpired for an expired token
// with a valid signature, and ErrTokenInvalid for anything else.
func (t *Tokens) Verify(token string) (identity.Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		key, ok := t.keys.public(kid)
		if !ok {
			return nil, fmt.Errorf("unknown key %q", kid)
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithTimeFunc(t.now),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return identity.Principal{}, ErrTokenExpired
	case err != nil:
		return identity.Principal{}, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}

	p := identity.Principal{Role: identity.Role(c.Role)}
	var errs [3]error
	p.UserID, errs[0] = uuid.Parse(c.Subject)
	p.TenantID, errs[1] = uuid.Parse(c.TenantID)
	p.SessionID, errs[2] = uuid.Parse(c.ID)
	if err := errors.Join(errs[:]...); err != nil || !p.Role.Valid() {
		return identity.Principal{}, fmt.Errorf("%w: malformed claims", ErrTokenInvalid)
	}
	return p, nil
}

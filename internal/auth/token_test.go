package auth_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/identity"
)

// seed returns a deterministic base64 Ed25519 seed.
func seed(b byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(rune('A'+b)), ed25519.SeedSize)))
}

func mustKeys(t *testing.T, spec string) *auth.KeySet {
	t.Helper()
	keys, err := auth.ParseKeySet(spec)
	if err != nil {
		t.Fatalf("ParseKeySet(%q) error = %v", spec, err)
	}
	return keys
}

var (
	testNow       = time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	testPrincipal = identity.Principal{
		UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleWarehouseManager, SessionID: uuid.New(),
	}
)

func clock(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func TestParseKeySet(t *testing.T) {
	rawURL := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
	valid := []string{
		"k1:" + seed(1),
		" k1:" + seed(1) + " , k2:" + seed(2) + ",",
		"2026-09.a_b:" + rawURL,
	}
	for _, spec := range valid {
		if _, err := auth.ParseKeySet(spec); err != nil {
			t.Errorf("ParseKeySet(%q) error = %v", spec, err)
		}
	}
	invalid := map[string]string{
		"":                                 "no signing key",
		"k1":                               "want <kid>",
		":" + seed(1):                      "want <kid>",
		"k 1:" + seed(1):                   "want <kid>",
		"k1:" + seed(1) + ",k1:" + seed(2): "listed twice",
		"k1:not base64!":                   "not base64",
		"k1:" + base64.StdEncoding.EncodeToString([]byte("short")): "has 5 bytes",
	}
	for spec, want := range invalid {
		if _, err := auth.ParseKeySet(spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseKeySet(%q) error = %v, want one mentioning %q", spec, err, want)
		}
	}
}

func TestIssueAndVerify(t *testing.T) {
	now := testNow
	tokens := auth.NewTokens(mustKeys(t, "k1:"+seed(1)), clock(&now))

	token, expiresAt, err := tokens.Issue(testPrincipal)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if !expiresAt.Equal(testNow.Add(auth.AccessTokenTTL)) {
		t.Errorf("expiresAt = %v, want now + 15 min", expiresAt)
	}

	got, err := tokens.Verify(token)
	if err != nil || got != testPrincipal {
		t.Fatalf("Verify() = %+v, %v; want %+v", got, err, testPrincipal)
	}

	// The claims are exactly those of ADR-0007.
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	for _, name := range []string{"sub", "tid", "role", "iat", "exp", "jti"} {
		if _, ok := claims[name]; !ok {
			t.Errorf("claim %s missing", name)
		}
	}
	if len(claims) != 6 || parsed.Header["kid"] != "k1" || parsed.Header["alg"] != "EdDSA" {
		t.Errorf("claims = %v, header = %v", claims, parsed.Header)
	}

	now = testNow.Add(auth.AccessTokenTTL - time.Second)
	if _, err := tokens.Verify(token); err != nil {
		t.Errorf("Verify() just before expiry error = %v", err)
	}
	now = testNow.Add(auth.AccessTokenTTL + time.Second)
	if _, err := tokens.Verify(token); !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("Verify() after expiry error = %v, want ErrTokenExpired", err)
	}
}

func TestVerifyAcrossKeyRotation(t *testing.T) {
	now := testNow
	before := auth.NewTokens(mustKeys(t, "old:"+seed(1)), clock(&now))
	after := auth.NewTokens(mustKeys(t, "new:"+seed(2)+",old:"+seed(1)), clock(&now))

	oldToken, _, _ := before.Issue(testPrincipal)
	if _, err := after.Verify(oldToken); err != nil {
		t.Errorf("a token signed by the previous key: error = %v", err)
	}
	newToken, _, _ := after.Issue(testPrincipal)
	parsed, _, _ := jwt.NewParser().ParseUnverified(newToken, jwt.MapClaims{})
	if parsed.Header["kid"] != "new" {
		t.Errorf("kid = %v, want the first key to sign", parsed.Header["kid"])
	}
	if _, err := before.Verify(newToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("a token signed by an unknown key: error = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyRejectsForgedTokens(t *testing.T) {
	now := testNow
	keys := mustKeys(t, "k1:"+seed(1))
	tokens := auth.NewTokens(keys, clock(&now))
	valid, _, _ := tokens.Issue(testPrincipal)

	good := jwt.MapClaims{
		"sub": testPrincipal.UserID.String(), "tid": testPrincipal.TenantID.String(), "role": "ADMIN",
		"iat": testNow.Unix(), "exp": testNow.Add(time.Minute).Unix(), "jti": uuid.NewString(),
	}
	_, otherKey, _ := ed25519.GenerateKey(nil)
	sign := func(method jwt.SigningMethod, key any, claims jwt.MapClaims, kid string) string {
		tok := jwt.NewWithClaims(method, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	with := func(edit func(jwt.MapClaims)) jwt.MapClaims {
		c := jwt.MapClaims{}
		for k, v := range good {
			c[k] = v
		}
		edit(c)
		return c
	}
	parts := strings.Split(valid, ".")

	forged := map[string]string{
		"alg none":         sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, good, "k1"),
		"hmac with public": sign(jwt.SigningMethodHS256, []byte(keys.JWKS().Keys[0].X), good, "k1"),
		"other key":        sign(jwt.SigningMethodEdDSA, otherKey, good, "k1"),
		"unknown kid":      sign(jwt.SigningMethodEdDSA, otherKey, good, "k9"),
		"tampered payload": parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + "." + parts[2],
		"not a jwt":        "abc.def",
		"bad subject":      sign(jwt.SigningMethodEdDSA, ed25519.NewKeyFromSeed([]byte(strings.Repeat("B", 32))), with(func(c jwt.MapClaims) { c["sub"] = "someone" }), "k1"),
		"bad role":         sign(jwt.SigningMethodEdDSA, ed25519.NewKeyFromSeed([]byte(strings.Repeat("B", 32))), with(func(c jwt.MapClaims) { c["role"] = "ROOT" }), "k1"),
		"no expiry":        sign(jwt.SigningMethodEdDSA, ed25519.NewKeyFromSeed([]byte(strings.Repeat("B", 32))), with(func(c jwt.MapClaims) { delete(c, "exp") }), "k1"),
		"issued later":     sign(jwt.SigningMethodEdDSA, ed25519.NewKeyFromSeed([]byte(strings.Repeat("B", 32))), with(func(c jwt.MapClaims) { c["iat"] = testNow.Add(time.Hour).Unix() }), "k1"),
	}
	for name, token := range forged {
		if _, err := tokens.Verify(token); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("%s: error = %v, want ErrTokenInvalid", name, err)
		}
	}
}

func TestJWKS(t *testing.T) {
	keys := mustKeys(t, "k1:"+seed(1)+",k2:"+seed(2))
	doc := keys.JWKS()
	if len(doc.Keys) != 2 || doc.Keys[0].KeyID != "k1" || doc.Keys[1].KeyID != "k2" {
		t.Fatalf("JWKS() = %+v", doc)
	}
	want := ed25519.NewKeyFromSeed([]byte(strings.Repeat("B", 32))).Public().(ed25519.PublicKey)
	k := doc.Keys[0]
	x, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil || !want.Equal(ed25519.PublicKey(x)) {
		t.Errorf("x = %q, want the public key of the seed", k.X)
	}
	if k.KeyType != "OKP" || k.Curve != "Ed25519" || k.Algorithm != "EdDSA" || k.Use != "sig" {
		t.Errorf("key = %+v", k)
	}
}

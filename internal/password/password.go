// Package password checks password rules and hashes passwords with Argon2id (security.md §2).
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Length limits in characters (OWASP ASVS 2.1.1 and 2.1.2).
const (
	MinLength = 12
	MaxLength = 128
)

// Password rule violations.
var (
	ErrTooShort = fmt.Errorf("password must be at least %d characters", MinLength)
	ErrTooLong  = fmt.Errorf("password must be at most %d characters", MaxLength)
)

// Validate checks a new password against the length rules. Any characters are allowed.
func Validate(password string) error {
	switch n := utf8.RuneCountInString(password); {
	case n < MinLength:
		return ErrTooShort
	case n > MaxLength:
		return ErrTooLong
	}
	return nil
}

// params are the Argon2id cost parameters.
type params struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

// current is the OWASP baseline: 19 MiB, 2 iterations, 1 lane.
var current = params{memoryKiB: 19 * 1024, time: 2, threads: 1}

const (
	saltLength = 16
	keyLength  = 32
)

// ErrMalformedHash reports a stored hash that is not an Argon2id PHC string.
var ErrMalformedHash = errors.New("malformed password hash")

// Hasher hashes and verifies passwords. Each Argon2id computation holds 19 MiB, so the hasher runs at most a
// fixed number at once and callers wait for a slot.
type Hasher struct {
	slots chan struct{}
	dummy string
}

// NewHasher returns a hasher that runs at most concurrency computations at once.
func NewHasher(concurrency int) (*Hasher, error) {
	if concurrency < 1 {
		return nil, fmt.Errorf("hash concurrency must be positive, got %d", concurrency)
	}
	h := &Hasher{slots: make(chan struct{}, concurrency)}
	dummy, err := h.Hash(context.Background(), rand.Text())
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the PHC string of password with a random salt.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLength)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(salt)

	key, err := h.derive(ctx, password, salt, current, keyLength)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, current.memoryKiB, current.time,
		current.threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded, in constant time. needsRehash reports that encoded uses
// other cost parameters than the current ones, so the caller should store a fresh hash.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (match, needsRehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	got, err := h.derive(ctx, password, salt, p, uint32(len(want))) //nolint:gosec // decode bounds the key to 64 bytes
	if err != nil {
		return false, false, err
	}
	stale := p != current || len(salt) != saltLength || len(want) != keyLength
	return subtle.ConstantTimeCompare(got, want) == 1, stale, nil
}

// VerifyDummy spends the same time as Verify against a hash that matches no password. Login calls it for
// unknown accounts, so response times do not reveal which accounts exist.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	_, _, err := h.Verify(ctx, password, h.dummy)
	return err
}

func (h *Hasher) derive(ctx context.Context, password string, salt []byte, p params, keyLen uint32) ([]byte, error) {
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for a hashing slot: %w", ctx.Err())
	}
	defer func() { <-h.slots }()
	return argon2.IDKey([]byte(password), salt, p.time, p.memoryKiB, p.threads, keyLen), nil
}

// decode parses "$argon2id$v=19$m=<KiB>,t=<iterations>,p=<lanes>$<salt>$<key>".
func decode(encoded string) (params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params{}, nil, nil, ErrMalformedHash
	}
	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.time, &p.threads); err != nil ||
		p.memoryKiB == 0 || p.time == 0 || p.threads == 0 {
		return params{}, nil, nil, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}

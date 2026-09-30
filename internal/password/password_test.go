package password_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/password"
)

func newHasher(t *testing.T) *password.Hasher {
	t.Helper()
	h, err := password.NewHasher(2)
	if err != nil {
		t.Fatalf("NewHasher() error = %v", err)
	}
	return h
}

func TestValidate(t *testing.T) {
	tests := []struct {
		password string
		want     error
	}{
		{strings.Repeat("a", 11), password.ErrTooShort},
		{strings.Repeat("a", 12), nil},
		{strings.Repeat("mật", 4), nil}, // 12 characters, 18 bytes
		{strings.Repeat("ậ", 11), password.ErrTooShort},
		{strings.Repeat("a", 128), nil},
		{strings.Repeat("a", 129), password.ErrTooLong},
	}
	for _, tt := range tests {
		if err := password.Validate(tt.password); !errors.Is(err, tt.want) {
			t.Errorf("Validate(%d runes) error = %v, want %v", len([]rune(tt.password)), err, tt.want)
		}
	}
}

func TestHashAndVerify(t *testing.T) {
	h := newHasher(t)
	ctx := t.Context()

	encoded, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("Hash() = %q, want an Argon2id PHC string with the OWASP parameters", encoded)
	}
	if again, _ := h.Hash(ctx, "correct horse battery"); again == encoded {
		t.Error("Hash() reused a salt")
	}

	match, stale, err := h.Verify(ctx, "correct horse battery", encoded)
	if err != nil || !match || stale {
		t.Errorf("Verify(correct) = %t, %t, %v; want match, current", match, stale, err)
	}
	if match, _, err := h.Verify(ctx, "correct horse batterY", encoded); err != nil || match {
		t.Errorf("Verify(wrong) = %t, %v; want no match", match, err)
	}
}

func TestVerifyFlagsOutdatedParameters(t *testing.T) {
	h := newHasher(t)
	encoded, err := h.Hash(t.Context(), "correct horse battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	// A hash made with one iteration still verifies, but must be replaced.
	parts := strings.Split(encoded, "$")
	parts[3] = "m=19456,t=1,p=1"
	match, stale, err := h.Verify(t.Context(), "correct horse battery", strings.Join(parts, "$"))
	if err != nil || match || !stale {
		t.Errorf("Verify() = %t, %t, %v; want no match (different key) and stale", match, stale, err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := newHasher(t)
	for _, encoded := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5",
		"$argon2id$v=16$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5",
		"$argon2id$v=19$m=19456,t=2$c2FsdHNhbHRzYWx0c2FsdA$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$!!$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$",
	} {
		if _, _, err := h.Verify(t.Context(), "x", encoded); !errors.Is(err, password.ErrMalformedHash) {
			t.Errorf("Verify(%q) error = %v, want ErrMalformedHash", encoded, err)
		}
	}
}

func TestVerifyDummy(t *testing.T) {
	if err := newHasher(t).VerifyDummy(t.Context(), "anything at all"); err != nil {
		t.Errorf("VerifyDummy() error = %v", err)
	}
}

func TestHashWaitsForASlot(t *testing.T) {
	h := newHasher(t)
	release := password.OccupySlots(h)
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.Hash(ctx, "correct horse battery"); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash() error = %v, want context.Canceled while every slot is taken", err)
	}
}

func TestNewHasherRejectsZeroConcurrency(t *testing.T) {
	if _, err := password.NewHasher(0); err == nil {
		t.Error("NewHasher(0) error = nil")
	}
}

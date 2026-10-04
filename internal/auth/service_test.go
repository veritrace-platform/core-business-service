package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// memStore is an in-memory Store. Changes apply immediately; transaction atomicity is covered by the
// integration tests.
type memStore struct {
	accounts map[string]auth.LoginUser // by lower-case email
	users    map[uuid.UUID]*user.Me
	hashes   map[uuid.UUID]string
	sessions map[uuid.UUID]*memSession
}

type memSession struct {
	auth.StoredSession
	tenantID  uuid.UUID
	tokenHash string
	userAgent *string
}

func (s *memStore) FindLoginUser(_ context.Context, email string) (auth.LoginUser, error) {
	a, ok := s.accounts[strings.ToLower(email)]
	if !ok {
		return auth.LoginUser{}, auth.ErrNotFound
	}
	a.PasswordHash = s.hashes[a.UserID]
	me := s.users[a.UserID]
	a.Active = me.IsActive && me.Tenant.Status == "ACTIVE"
	return a, nil
}

func (s *memStore) FindSession(_ context.Context, tokenHash []byte) (auth.SessionRef, error) {
	for _, sess := range s.sessions {
		if sess.tokenHash == string(tokenHash) {
			return auth.SessionRef{SessionID: sess.ID, FamilyID: sess.FamilyID, TenantID: sess.tenantID, UserID: sess.UserID}, nil
		}
	}
	return auth.SessionRef{}, auth.ErrNotFound
}

func (s *memStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(auth.Repository) error) error {
	return fn(memRepo{s})
}

type memRepo struct{ s *memStore }

func (r memRepo) CreateSession(_ context.Context, n auth.NewSession) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	r.s.sessions[id] = &memSession{
		StoredSession: auth.StoredSession{ID: id, FamilyID: n.FamilyID, UserID: n.UserID, ExpiresAt: n.ExpiresAt},
		tenantID:      n.TenantID, tokenHash: string(n.TokenHash), userAgent: n.UserAgent,
	}
	return id, nil
}

func (r memRepo) LockSession(_ context.Context, id uuid.UUID) (auth.StoredSession, error) {
	sess, ok := r.s.sessions[id]
	if !ok {
		return auth.StoredSession{}, auth.ErrNotFound
	}
	return sess.StoredSession, nil
}

func (r memRepo) SessionFamily(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	sess, ok := r.s.sessions[id]
	if !ok {
		return uuid.Nil, auth.ErrNotFound
	}
	return sess.FamilyID, nil
}

func (r memRepo) MarkRotated(_ context.Context, id uuid.UUID, at time.Time) error {
	r.s.sessions[id].RotatedAt = &at
	return nil
}

func (r memRepo) RevokeFamily(_ context.Context, family uuid.UUID, at time.Time) error {
	for _, sess := range r.s.sessions {
		if sess.FamilyID == family && sess.RevokedAt == nil {
			sess.RevokedAt = &at
		}
	}
	return nil
}

func (r memRepo) RevokeOtherSessions(_ context.Context, userID, keep uuid.UUID, at time.Time) error {
	for _, sess := range r.s.sessions {
		if sess.UserID == userID && sess.FamilyID != keep && sess.RevokedAt == nil {
			sess.RevokedAt = &at
		}
	}
	return nil
}

func (r memRepo) DeleteExpiredFamily(_ context.Context, family uuid.UUID, before time.Time) error {
	for id, sess := range r.s.sessions {
		if sess.FamilyID == family && sess.ExpiresAt.Before(before) {
			delete(r.s.sessions, id)
		}
	}
	return nil
}

func (r memRepo) DeleteExpiredForUser(_ context.Context, userID uuid.UUID, before time.Time) error {
	for id, sess := range r.s.sessions {
		if sess.UserID == userID && sess.ExpiresAt.Before(before) {
			delete(r.s.sessions, id)
		}
	}
	return nil
}

func (r memRepo) RecordLogin(_ context.Context, userID uuid.UUID, at time.Time) error {
	r.s.users[userID].LastLoginAt = &at
	return nil
}

func (r memRepo) PasswordHash(_ context.Context, userID uuid.UUID) (string, error) {
	hash, ok := r.s.hashes[userID]
	if !ok {
		return "", auth.ErrNotFound
	}
	return hash, nil
}

func (r memRepo) SetPasswordHash(_ context.Context, userID uuid.UUID, hash string) error {
	r.s.hashes[userID] = hash
	return nil
}

func (r memRepo) Me(_ context.Context, userID uuid.UUID) (user.Me, error) {
	me, ok := r.s.users[userID]
	if !ok {
		return user.Me{}, auth.ErrNotFound
	}
	return *me, nil
}

// fakeHasher stores "hash:<password>"; hashes starting with "old:" verify but are stale.
type fakeHasher struct{ dummyCalls int }

func (h *fakeHasher) Hash(_ context.Context, password string) (string, error) {
	return "hash:" + password, nil
}

func (h *fakeHasher) Verify(_ context.Context, password, encoded string) (bool, bool, error) {
	if rest, ok := strings.CutPrefix(encoded, "old:"); ok {
		return rest == password, true, nil
	}
	return encoded == "hash:"+password, false, nil
}

func (h *fakeHasher) VerifyDummy(context.Context, string) error {
	h.dummyCalls++
	return nil
}

type fixture struct {
	store  *memStore
	hasher *fakeHasher
	svc    *auth.Service
	tokens *auth.Tokens
	now    *time.Time
	me     *user.Me
}

const (
	testEmail    = "an@sgfresh.example"
	testPassword = "correct horse battery"
	refreshTTL   = 7 * 24 * time.Hour
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := testNow
	me := &user.Me{
		User:   user.User{ID: uuid.New(), Email: testEmail, Role: identity.RoleWarehouseManager, IsActive: true},
		Tenant: user.TenantSummary{ID: uuid.New(), Code: "SGFRESH", Status: "ACTIVE"},
	}
	store := &memStore{
		accounts: map[string]auth.LoginUser{testEmail: {UserID: me.ID, TenantID: me.Tenant.ID, Role: me.Role}},
		users:    map[uuid.UUID]*user.Me{me.ID: me},
		hashes:   map[uuid.UUID]string{me.ID: "hash:" + testPassword},
		sessions: map[uuid.UUID]*memSession{},
	}
	hasher := &fakeHasher{}
	tokens := auth.NewTokens(mustKeys(t, "k1:"+seed(1)), clock(&now))
	return &fixture{
		store: store, hasher: hasher, tokens: tokens, now: &now, me: me,
		svc: auth.NewService(store, hasher, tokens, refreshTTL, clock(&now)),
	}
}

func (f *fixture) login(t *testing.T) auth.Session {
	t.Helper()
	ua := "Mozilla/5.0"
	s, err := f.svc.Login(t.Context(), "AN@SGFresh.example", testPassword, &ua)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	return s
}

// sessionOf returns the stored session that issued s, read from the access token's jti without checking its
// expiry.
func (f *fixture) sessionOf(t *testing.T, s auth.Session) *memSession {
	t.Helper()
	var c jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(s.AccessToken, &c); err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	return f.store.sessions[uuid.MustParse(c.ID)]
}

func TestLogin(t *testing.T) {
	f := newFixture(t)
	s := f.login(t)

	p, err := f.tokens.Verify(s.AccessToken)
	if err != nil || p.UserID != f.me.ID || p.TenantID != f.me.Tenant.ID || p.Role != identity.RoleWarehouseManager {
		t.Fatalf("access token principal = %+v, %v", p, err)
	}
	stored := f.store.sessions[p.SessionID]
	if stored == nil || stored.ExpiresAt != testNow.Add(refreshTTL) || *stored.userAgent != "Mozilla/5.0" {
		t.Fatalf("stored session = %+v, want one expiring after the refresh TTL, tagged with the user agent", stored)
	}
	if s.RefreshToken == "" || s.AccessTokenTTL != auth.AccessTokenTTL || s.RefreshTokenTTL != refreshTTL {
		t.Errorf("session = %+v", s)
	}
	if s.User.LastLoginAt == nil || !s.User.LastLoginAt.Equal(testNow) {
		t.Errorf("last login = %v, want now", s.User.LastLoginAt)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Login(t.Context(), "nobody@sgfresh.example", testPassword, nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unknown email: error = %v", err)
	}
	if f.hasher.dummyCalls != 1 {
		t.Errorf("dummy verifications = %d, want 1 for the unknown email", f.hasher.dummyCalls)
	}
	if _, err := f.svc.Login(t.Context(), testEmail, "wrong password", nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password: error = %v", err)
	}

	f.me.IsActive = false
	if _, err := f.svc.Login(t.Context(), testEmail, testPassword, nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("inactive user: error = %v", err)
	}
	f.me.IsActive = true
	f.me.Tenant.Status = "SUSPENDED"
	if _, err := f.svc.Login(t.Context(), testEmail, testPassword, nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("suspended tenant: error = %v", err)
	}
	if len(f.store.sessions) != 0 {
		t.Errorf("%d sessions were created by failed logins", len(f.store.sessions))
	}
}

func TestLoginRehashesStalePasswords(t *testing.T) {
	f := newFixture(t)
	f.store.hashes[f.me.ID] = "old:" + testPassword
	f.login(t)
	if got := f.store.hashes[f.me.ID]; got != "hash:"+testPassword {
		t.Errorf("stored hash = %q, want a fresh one", got)
	}
}

func TestRefreshRotatesTheToken(t *testing.T) {
	f := newFixture(t)
	first := f.login(t)
	*f.now = testNow.Add(time.Hour)

	second, err := f.svc.Refresh(t.Context(), first.RefreshToken, nil)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	old, current := f.sessionOf(t, first), f.sessionOf(t, second)
	if old.RotatedAt == nil || current.RotatedAt != nil || current.FamilyID != old.FamilyID || current.ID == old.ID {
		t.Errorf("old = %+v, new = %+v; want the old one rotated and a new one in the same family", old, current)
	}
	if second.RefreshToken == first.RefreshToken || !current.ExpiresAt.Equal(testNow.Add(time.Hour+refreshTTL)) {
		t.Errorf("new refresh token = %q expiring %v", second.RefreshToken, current.ExpiresAt)
	}
}

func TestRefreshDetectsReuse(t *testing.T) {
	f := newFixture(t)
	first := f.login(t)
	second, err := f.svc.Refresh(t.Context(), first.RefreshToken, nil)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if _, err := f.svc.Refresh(t.Context(), first.RefreshToken, nil); !errors.Is(err, auth.ErrRefreshTokenReused) ||
		!errors.Is(err, auth.ErrRefreshTokenInvalid) {
		t.Fatalf("reusing a rotated token: error = %v, want ErrRefreshTokenReused", err)
	}
	for _, sess := range f.store.sessions {
		if sess.RevokedAt == nil {
			t.Errorf("session %s survived the reuse", sess.ID)
		}
	}
	if _, err := f.svc.Refresh(t.Context(), second.RefreshToken, nil); !errors.Is(err, auth.ErrRefreshTokenInvalid) {
		t.Errorf("the newest token after reuse: error = %v, want ErrRefreshTokenInvalid", err)
	}
}

func TestRefreshRejects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture, s auth.Session) string // returns the token to present
	}{
		{"malformed token", func(*fixture, auth.Session) string { return "not-a-token" }},
		{"unknown token", func(*fixture, auth.Session) string { return strings.Repeat("A", 43) }},
		{"expired token", func(f *fixture, s auth.Session) string {
			*f.now = testNow.Add(refreshTTL)
			return s.RefreshToken
		}},
		{"revoked token", func(f *fixture, s auth.Session) string {
			if err := f.svc.Logout(context.Background(), s.RefreshToken); err != nil {
				panic(err)
			}
			return s.RefreshToken
		}},
		{"deactivated user", func(f *fixture, s auth.Session) string {
			f.me.IsActive = false
			return s.RefreshToken
		}},
		{"suspended tenant", func(f *fixture, s auth.Session) string {
			f.me.Tenant.Status = "SUSPENDED"
			return s.RefreshToken
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			s := f.login(t)
			token := tt.setup(f, s)
			if _, err := f.svc.Refresh(t.Context(), token, nil); !errors.Is(err, auth.ErrRefreshTokenInvalid) ||
				errors.Is(err, auth.ErrRefreshTokenReused) {
				t.Errorf("error = %v, want ErrRefreshTokenInvalid", err)
			}
		})
	}
}

func TestRefreshRevokesFamilyOfBlockedAccounts(t *testing.T) {
	f := newFixture(t)
	s := f.login(t)
	f.me.IsActive = false
	_, _ = f.svc.Refresh(t.Context(), s.RefreshToken, nil)
	if f.sessionOf(t, s).RevokedAt == nil {
		t.Error("the family of a deactivated user was not revoked")
	}
}

func TestLogout(t *testing.T) {
	f := newFixture(t)
	s := f.login(t)
	if err := f.svc.Logout(t.Context(), s.RefreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if f.sessionOf(t, s).RevokedAt == nil {
		t.Error("the session was not revoked")
	}
	for _, token := range []string{s.RefreshToken, strings.Repeat("A", 43), "garbage"} {
		if err := f.svc.Logout(t.Context(), token); err != nil {
			t.Errorf("Logout(%q) error = %v, want idempotent success", token, err)
		}
	}
}

func TestMe(t *testing.T) {
	f := newFixture(t)
	s := f.login(t)
	p, _ := f.tokens.Verify(s.AccessToken)
	me, err := f.svc.Me(t.Context(), p)
	if err != nil || me.ID != f.me.ID || me.Tenant.Code != "SGFRESH" {
		t.Errorf("Me() = %+v, %v", me, err)
	}
	p.UserID = uuid.New()
	if _, err := f.svc.Me(t.Context(), p); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("Me() for a missing user: error = %v, want ErrNotFound", err)
	}
}

func TestChangePassword(t *testing.T) {
	f := newFixture(t)
	mine, other := f.login(t), f.login(t)
	p, _ := f.tokens.Verify(mine.AccessToken)

	if err := f.svc.ChangePassword(t.Context(), p, "wrong password", "a brand new password"); !errors.Is(err, auth.ErrIncorrectPassword) {
		t.Fatalf("wrong current password: error = %v", err)
	}
	if err := f.svc.ChangePassword(t.Context(), p, testPassword, "a brand new password"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if f.store.hashes[f.me.ID] != "hash:a brand new password" {
		t.Error("the new password was not stored")
	}
	if f.sessionOf(t, mine).RevokedAt != nil {
		t.Error("the session that changed the password was revoked")
	}
	if f.sessionOf(t, other).RevokedAt == nil {
		t.Error("another session survived the password change")
	}
}

package tenant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/tenant"
)

type fakeHasher struct{ err error }

func (f fakeHasher) Hash(_ context.Context, password string) (string, error) {
	return "hash:" + password, f.err
}

type fakeRegistrar struct {
	gotHash string
	err     error
}

func (f *fakeRegistrar) RegisterTenant(_ context.Context, r tenant.Registration, hash string) (tenant.Registered, error) {
	f.gotHash = hash
	var out tenant.Registered
	out.Tenant.Code = r.Code
	return out, f.err
}

func TestServiceRegisterStoresThePasswordHash(t *testing.T) {
	registrar := &fakeRegistrar{}
	svc := tenant.NewService(registrar, nil, fakeHasher{})

	got, err := svc.Register(t.Context(), tenant.Registration{Code: "SGFRESH", Admin: tenant.Admin{Password: "secret password"}})
	if err != nil || got.Tenant.Code != "SGFRESH" {
		t.Fatalf("Register() = %+v, %v", got, err)
	}
	if registrar.gotHash != "hash:secret password" {
		t.Errorf("stored %q, want the hash of the admin password", registrar.gotHash)
	}
}

func TestServiceRegisterPassesErrorsThrough(t *testing.T) {
	conflict := &tenant.ConflictError{Key: tenant.KeyCode}
	svc := tenant.NewService(&fakeRegistrar{err: conflict}, nil, fakeHasher{})
	var got *tenant.ConflictError
	if _, err := svc.Register(t.Context(), tenant.Registration{}); !errors.As(err, &got) || got.Key != tenant.KeyCode {
		t.Errorf("Register() error = %v, want the conflict", err)
	}

	hashErr := errors.New("no slot")
	svc = tenant.NewService(&fakeRegistrar{}, nil, fakeHasher{err: hashErr})
	if _, err := svc.Register(t.Context(), tenant.Registration{}); !errors.Is(err, hashErr) {
		t.Errorf("Register() error = %v, want the hashing error", err)
	}
}

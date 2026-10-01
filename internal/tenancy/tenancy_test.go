package tenancy_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

var tenantID = uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e")

// fakePool hands out a single fakeTx.
type fakePool struct {
	tx       *fakeTx
	beginErr error
}

func (p *fakePool) Begin(context.Context) (pgx.Tx, error) {
	p.tx.calls = append(p.tx.calls, "begin")
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return p.tx, nil
}

// fakeTx records the calls WithTenantTx makes. The embedded pgx.Tx is nil, so calling any other method
// panics and fails the test.
type fakeTx struct {
	pgx.Tx
	execErr   error
	commitErr error
	calls     []string
	execSQL   string
	execArgs  []any
	closed    bool
}

func (tx *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.calls = append(tx.calls, "set tenant")
	tx.execSQL, tx.execArgs = sql, args
	return pgconn.NewCommandTag("SELECT 1"), tx.execErr
}

// Commit closes the transaction whether or not it succeeds, as pgx does.
func (tx *fakeTx) Commit(context.Context) error {
	tx.calls = append(tx.calls, "commit")
	tx.closed = true
	return tx.commitErr
}

func (tx *fakeTx) Rollback(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}
	tx.calls = append(tx.calls, "rollback")
	tx.closed = true
	return nil
}

func TestWithTenantTx(t *testing.T) {
	errDomain := errors.New("domain rule violated")
	errConn := errors.New("connection reset")

	tests := []struct {
		name      string
		beginErr  error
		execErr   error
		fnErr     error
		commitErr error
		wantErr   error
		wantMsg   string
		wantCalls []string
	}{
		{
			name:      "commits when fn succeeds",
			wantCalls: []string{"begin", "set tenant", "fn", "commit"},
		},
		{
			name:      "rolls back and returns the fn error unchanged",
			fnErr:     errDomain,
			wantErr:   errDomain,
			wantMsg:   errDomain.Error(),
			wantCalls: []string{"begin", "set tenant", "fn", "rollback"},
		},
		{
			name:      "skips fn when the tenant context cannot be set",
			execErr:   errConn,
			wantErr:   errConn,
			wantMsg:   "set tenant context: ",
			wantCalls: []string{"begin", "set tenant", "rollback"},
		},
		{
			name:      "reports a failed commit",
			commitErr: errConn,
			wantErr:   errConn,
			wantMsg:   "commit tenant transaction: ",
			wantCalls: []string{"begin", "set tenant", "fn", "commit"},
		},
		{
			name:      "reports a failed begin",
			beginErr:  errConn,
			wantErr:   errConn,
			wantMsg:   "begin tenant transaction: ",
			wantCalls: []string{"begin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &fakeTx{execErr: tt.execErr, commitErr: tt.commitErr}
			db := tenancy.NewDB(&fakePool{tx: tx, beginErr: tt.beginErr})

			err := db.WithTenantTx(t.Context(), tenantID, func(got pgx.Tx) error {
				if got != tx {
					t.Errorf("fn received %v, want the transaction that set the tenant context", got)
				}
				tx.calls = append(tx.calls, "fn")
				return tt.fnErr
			})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WithTenantTx() error = %v, want %v", err, tt.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), tt.wantMsg) {
				t.Errorf("WithTenantTx() error = %q, want prefix %q", err, tt.wantMsg)
			}
			if !slices.Equal(tx.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", tx.calls, tt.wantCalls)
			}
		})
	}
}

func TestWithTenantTxSetsTransactionLocalContext(t *testing.T) {
	tx := &fakeTx{}
	db := tenancy.NewDB(&fakePool{tx: tx})

	if err := db.WithTenantTx(t.Context(), tenantID, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithTenantTx() error = %v", err)
	}
	if want := `SELECT set_config('app.current_tenant_id', $1, true)`; tx.execSQL != want {
		t.Errorf("tenant context SQL = %q, want %q", tx.execSQL, want)
	}
	if want := []any{tenantID.String()}; !slices.Equal(tx.execArgs, want) {
		t.Errorf("tenant context args = %v, want %v", tx.execArgs, want)
	}
}

func TestWithTenantTxRejectsMissingTenant(t *testing.T) {
	tx := &fakeTx{}
	db := tenancy.NewDB(&fakePool{tx: tx})

	err := db.WithTenantTx(t.Context(), uuid.Nil, func(pgx.Tx) error {
		t.Error("fn must not run without a tenant")
		return nil
	})
	if !errors.Is(err, tenancy.ErrNoTenant) {
		t.Fatalf("WithTenantTx() error = %v, want %v", err, tenancy.ErrNoTenant)
	}
	if len(tx.calls) != 0 {
		t.Errorf("calls = %v, want no database access", tx.calls)
	}
}

func TestWithTenantTxRollsBackWhenFnPanics(t *testing.T) {
	tx := &fakeTx{}
	db := tenancy.NewDB(&fakePool{tx: tx})

	defer func() {
		if r := recover(); r != "boom" {
			t.Errorf("recovered %v, want the panic from fn to propagate", r)
		}
		if want := []string{"begin", "set tenant", "rollback"}; !slices.Equal(tx.calls, want) {
			t.Errorf("calls = %v, want %v", tx.calls, want)
		}
	}()
	_ = db.WithTenantTx(t.Context(), tenantID, func(pgx.Tx) error { panic("boom") })
}

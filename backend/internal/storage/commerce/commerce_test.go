package commercestore

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
)

// mapWriteError is the boundary between SQLSTATE and the domain's vocabulary, and it
// sits on the success path of every write: wrapping a nil error would turn every
// successful insert into a reported failure. The tests here hold the boundary from
// both sides.
func TestMapWriteErrorLeavesNilAlone(t *testing.T) {
	if err := mapWriteError(nil); err != nil {
		t.Fatalf("a successful write was reported as %v", err)
	}
}

func TestMapWriteErrorMapsAUniqueViolationToConflict(t *testing.T) {
	pgErr := &pgconn.PgError{Code: uniqueViolation, ConstraintName: "orders_order_no_key"}
	err := mapWriteError(pgErr)
	if !errors.Is(err, commerce.ErrConflict) {
		t.Fatalf("a unique violation produced %v", err)
	}

	// The idempotency key names a second payment attempt for the same order, which the
	// caller distinguishes from a number collision.
	pgErr.ConstraintName = "payments_idempotency_key_key"
	if err := mapWriteError(pgErr); !errors.Is(err, commerce.ErrPaymentAlreadyStarted) {
		t.Fatalf("an idempotency-key collision produced %v", err)
	}
}

func TestMapWriteErrorKeepsEverythingElseAStorageFailure(t *testing.T) {
	// A CHECK violation is not the caller's input being wrong, so it must NOT become a
	// conflict: the caller would retry a request that will never succeed.
	pgErr := &pgconn.PgError{Code: "23514", ConstraintName: "orders_total_is_subtotal_less_discount"}
	err := mapWriteError(pgErr)
	if errors.Is(err, commerce.ErrConflict) {
		t.Fatal("a CHECK violation was reported as a conflict")
	}
	if err == nil || !errors.Is(err, errCauseOf(err)) {
		t.Fatalf("the underlying failure was not preserved: %v", err)
	}
}

// errCauseOf unwraps once, so the test can see the cause is still inside.
func errCauseOf(err error) error { return errors.Unwrap(err) }

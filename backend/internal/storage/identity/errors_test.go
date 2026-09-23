package identitystore

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// These tests cover the part of the adapter that can be wrong without a database:
// the translation between the driver's vocabulary and the domain's. The statements
// themselves are proven by the integration tier, which runs against PostgreSQL in CI.

func TestMapLookupErrorSeparatesAbsenceFromFailure(t *testing.T) {
	// Absence is a fact the service acts on: it decides what a caller may learn.
	if err := mapLookupError(pgx.ErrNoRows); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("ErrNoRows mapped to %v", err)
	}
	// Wrapped, too: the driver does not promise to return it bare.
	if err := mapLookupError(fmt.Errorf("query: %w", pgx.ErrNoRows)); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("a wrapped ErrNoRows mapped to %v", err)
	}

	// A broken connection is not "no such account". Collapsing the two would make the
	// service tell a caller their credentials are wrong when the database is down.
	failure := errors.New("connection reset")
	mapped := mapLookupError(failure)
	if errors.Is(mapped, identity.ErrUserNotFound) {
		t.Error("a connection failure was reported as a missing account")
	}
	if !errors.Is(mapped, failure) {
		t.Error("the underlying cause was not preserved")
	}
}

func TestMapWriteErrorRecognisesOnlyTheAddressConflict(t *testing.T) {
	if err := mapWriteError(&pgconn.PgError{Code: uniqueViolation}); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("a unique violation mapped to %v", err)
	}

	// Foreign key and check violations are not conflicts with the user's input, so
	// they must not be reported as one: the caller would be told to try a different
	// address for a problem that has nothing to do with the address.
	for _, code := range []string{"23503", "23514", "08006"} {
		err := mapWriteError(&pgconn.PgError{Code: code})
		if errors.Is(err, identity.ErrEmailTaken) {
			t.Errorf("SQLSTATE %s was reported as an address conflict", code)
		}
	}

	plain := errors.New("connection reset")
	if mapped := mapWriteError(plain); errors.Is(mapped, identity.ErrEmailTaken) || !errors.Is(mapped, plain) {
		t.Errorf("a plain error mapped to %v", mapped)
	}
}

func TestUUIDOrNil(t *testing.T) {
	if got := uuidOrNil(uuid.Nil); got != nil {
		t.Errorf("the zero UUID became %v; the column is nullable and a zero value would "+
			"reference nothing", got)
	}

	id := uuid.MustParse("0198f1c2-0000-7000-8000-000000000001")
	got := uuidOrNil(id)
	if got == nil || *got != id {
		t.Errorf("a real identifier became %v", got)
	}
}

func TestTextOrNull(t *testing.T) {
	// Empty and absent mean the same thing for these columns; storing "" would make
	// "not recorded" indistinguishable from "recorded as empty".
	if empty := textOrNull(""); empty.Valid {
		t.Error("an empty string was stored as a value rather than as NULL")
	}

	value := textOrNull("Go-http-client/1.1")
	if !value.Valid || value.String != "Go-http-client/1.1" {
		t.Errorf("a non-empty string became %+v", value)
	}
}

func TestParseIP(t *testing.T) {
	cases := map[string]bool{
		"203.0.113.7":   true,
		"2001:db8::1":   true,
		"":              false,
		"not-an-ip":     false,
		"203.0.113.7:9": false,
	}
	for input, want := range cases {
		got := parseIP(input)
		if want && got == nil {
			t.Errorf("parseIP(%q) was dropped", input)
			continue
		}
		if !want && got != nil {
			t.Errorf("parseIP(%q) produced %v", input, got)
			continue
		}
		if want && !got.IsValid() {
			t.Errorf("parseIP(%q) is not a valid address", input)
		}
	}

	// The concrete value matters: an operator filters the trail by address.
	got := parseIP("203.0.113.7")
	if got == nil || *got != netip.MustParseAddr("203.0.113.7") {
		t.Errorf("parseIP returned %v", got)
	}
}

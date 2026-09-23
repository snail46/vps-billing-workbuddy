// Package identitystore adapts the generated queries to the interfaces the identity
// package declares.
//
// It is a package of its own rather than part of internal/db because that package
// states its boundary explicitly: it owns connection management and never knows about
// a table. This one exists precisely to know about the identity tables.
//
// Everything database-specific stops here. The SQLSTATE codes, the handling of NULL,
// the driver's row type — none of it escapes, so the service's rules stay expressed in
// its own vocabulary and remain testable without a database.
package identitystore

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// uniqueViolation is PostgreSQL's SQLSTATE for a unique constraint violation.
const uniqueViolation = "23505"

// mapLookupError turns "the query matched no rows" into the domain's not-found error.
//
// The domain distinguishes "no such account" from "the lookup failed", which is what
// lets the service decide what a caller may learn — and only this layer can make that
// distinction, because only this layer sees the driver's row handling.
func mapLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrUserNotFound
	}
	return fmt.Errorf("identitystore: lookup failed: %w", err)
}

// mapWriteError turns a uniqueness violation into the domain's conflict.
//
// Both identity tables have exactly one unique constraint, on the address, so a
// violation can only mean that address is taken. Keeping the SQLSTATE here is what
// allows the service to return ErrEmailTaken without ever having seen a driver error.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return identity.ErrEmailTaken
	}
	return fmt.Errorf("identitystore: write failed: %w", err)
}

// uuidOrNil renders the zero UUID as SQL NULL.
//
// The audit table's actor and resource columns are nullable: a system action has no
// actor, and some resources are identified by type alone. Writing the zero UUID
// instead would put a value into the column that no foreign key references and that
// every reader would have to know to ignore.
func uuidOrNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// textOrNull renders an empty string as SQL NULL.
//
// Empty and absent are the same thing for these columns — an unrecorded user agent,
// an unrecorded request id — and storing "" would make "not recorded" indistinguishable
// from "recorded as empty" in a query.
func textOrNull(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// parseIP turns a source address into the type the inet column takes.
//
// An address that cannot be parsed is dropped rather than rejected: the audit entry is
// more valuable than the address attached to it, and a proxy reformatting a header
// must not be able to stop a security-relevant record from being written.
func parseIP(value string) *netip.Addr {
	if value == "" {
		return nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return nil
	}
	return &addr
}

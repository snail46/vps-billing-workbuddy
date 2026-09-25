package identitystore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// Directory reads and writes identity records.
type Directory struct{ queries *sqlcgen.Queries }

// NewDirectory returns a directory over a connection or a transaction.
//
// Taking sqlcgen.DBTX rather than a pool is deliberate: a phase that must write
// something and its audit entry in one transaction needs to build the directory from
// the transaction, and passing the pool would make that impossible without a second
// constructor.
func NewDirectory(db sqlcgen.DBTX) *Directory { return &Directory{queries: sqlcgen.New(db)} }

var (
	_ identity.Directory          = (*Directory)(nil)
	_ identity.PermissionResolver = (*Directory)(nil)
)

// CreateUser persists a new user.
func (d *Directory) CreateUser(ctx context.Context, user identity.User) (identity.User, error) {
	row, err := d.queries.CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:           user.ID,
		Email:        user.Email,
		PasswordHash: user.PasswordHash,
		Status:       user.Status,
		Locale:       user.Locale,
		Timezone:     user.Timezone,
	})
	if err != nil {
		return identity.User{}, mapWriteError(err)
	}
	return toUser(row), nil
}

// FindUserByEmail looks a user up by address.
func (d *Directory) FindUserByEmail(ctx context.Context, email string) (identity.User, error) {
	row, err := d.queries.GetUserByEmail(ctx, email)
	if err != nil {
		return identity.User{}, mapLookupError(err, identity.ErrUserNotFound)
	}
	return toUser(row), nil
}

// FindUserByID looks a user up by identifier.
func (d *Directory) FindUserByID(ctx context.Context, id uuid.UUID) (identity.User, error) {
	row, err := d.queries.GetUserByID(ctx, id)
	if err != nil {
		return identity.User{}, mapLookupError(err, identity.ErrUserNotFound)
	}
	return toUser(row), nil
}

// RecordUserLogin updates the last-login timestamp.
func (d *Directory) RecordUserLogin(ctx context.Context, id uuid.UUID) error {
	if err := d.queries.RecordUserLogin(ctx, id); err != nil {
		return fmt.Errorf("identitystore: record user login: %w", err)
	}
	return nil
}

// UpdateUserPasswordHash replaces the stored hash.
func (d *Directory) UpdateUserPasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	if err := d.queries.UpdateUserPasswordHash(ctx, sqlcgen.UpdateUserPasswordHashParams{
		ID:           id,
		PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("identitystore: update user password hash: %w", err)
	}
	return nil
}

// FindAdminByEmail looks an administrator up by address.
func (d *Directory) FindAdminByEmail(ctx context.Context, email string) (identity.Admin, error) {
	row, err := d.queries.GetAdminByEmail(ctx, email)
	if err != nil {
		return identity.Admin{}, mapLookupError(err, identity.ErrAdminNotFound)
	}
	return toAdmin(row), nil
}

// FindAdminByID looks an administrator up by identifier.
func (d *Directory) FindAdminByID(ctx context.Context, id uuid.UUID) (identity.Admin, error) {
	row, err := d.queries.GetAdminByID(ctx, id)
	if err != nil {
		return identity.Admin{}, mapLookupError(err, identity.ErrAdminNotFound)
	}
	return toAdmin(row), nil
}

// RecordAdminLogin updates the last-login timestamp.
func (d *Directory) RecordAdminLogin(ctx context.Context, id uuid.UUID) error {
	if err := d.queries.RecordAdminLogin(ctx, id); err != nil {
		return fmt.Errorf("identitystore: record admin login: %w", err)
	}
	return nil
}

// UpdateAdminPasswordHash replaces the stored hash.
func (d *Directory) UpdateAdminPasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	if err := d.queries.UpdateAdminPasswordHash(ctx, sqlcgen.UpdateAdminPasswordHashParams{
		ID:           id,
		PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("identitystore: update admin password hash: %w", err)
	}
	return nil
}

// PermissionsForAdmin resolves the effective permission set of an administrator.
//
// The union over the admin's roles is computed by the query rather than here, so a
// role with no permissions, an admin with no roles, and a permission granted twice
// all resolve without the caller having to know which is which. The result is ordered
// by key so it can be compared directly in a test.
func (d *Directory) PermissionsForAdmin(ctx context.Context, adminID uuid.UUID) ([]string, error) {
	keys, err := d.queries.ListPermissionsForAdmin(ctx, adminID)
	if err != nil {
		return nil, fmt.Errorf("identitystore: list permissions: %w", err)
	}
	// An administrator with no roles has no permissions. Returning a non-nil empty
	// slice keeps "no permissions" and "the query did not run" distinguishable to a
	// caller that checks for nil.
	if keys == nil {
		return []string{}, nil
	}
	return keys, nil
}

func toUser(row sqlcgen.User) identity.User {
	return identity.User{
		ID:           row.ID,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		Status:       row.Status,
		Locale:       row.Locale,
		Timezone:     row.Timezone,
	}
}

func toAdmin(row sqlcgen.Admin) identity.Admin {
	admin := identity.Admin{
		ID:               row.ID,
		Email:            row.Email,
		PasswordHash:     row.PasswordHash,
		Status:           row.Status,
		TwoFactorEnabled: row.TwoFactorEnabled,
	}
	if row.DisplayName.Valid {
		admin.DisplayName = row.DisplayName.String
	}
	if row.TwoFactorSecret.Valid {
		admin.TwoFactorSecret = row.TwoFactorSecret.String
	}
	return admin
}

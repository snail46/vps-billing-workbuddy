package identitystore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// The integration tier. It needs the migrated schema and is skipped without it, because what
// it covers is the part a fake cannot reach: that the SQL is valid, that the statements return
// what they are read as, and that the constraints behave the way the adapter assumes. The
// translation from driver errors to domain errors is covered by errors_test.go, against
// synthetic errors; here the same mappings are exercised against real ones.
//
// The CI integration job migrates a PostgreSQL service and sets TEST_DATABASE_URL. Locally
// the tests are skipped (ADR-003).

// newPool connects to the test database, or skips the test.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("the test database is not reachable: %v", err)
	}
	return pool
}

// uniqueAddress returns an address no other run will have used.
//
// Lower case, because the schema refuses anything else: normalisation is the storing
// application's job and the CHECK constraint is what proves it happened. Addresses are also
// UNIQUE, so a fixed one would pass once and fail on the second run of a database that was
// not recreated in between — which is exactly the sort of test that gets blamed on the code
// under test.
func uniqueAddress() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return strings.ToLower(base64.RawURLEncoding.EncodeToString(buf)) + "@example.com"
}

// purge removes the rows a test created. It runs after the assertions, so a failure leaves
// the evidence in place.
func purge(t *testing.T, pool *pgxpool.Pool, addresses ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, address := range addresses {
			// Users and admins are referenced by audit rows only through nullable columns
			// with no foreign key, so the order here does not matter.
			_, _ = pool.Exec(ctx, "DELETE FROM admin_roles WHERE admin_id IN (SELECT id FROM admins WHERE email = $1)", address)
			_, _ = pool.Exec(ctx, "DELETE FROM users WHERE email = $1", address)
			_, _ = pool.Exec(ctx, "DELETE FROM admins WHERE email = $1", address)
		}
	})
}

func TestUserStatementsRoundTrip(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	address := uniqueAddress()
	purge(t, pool, address)

	created, err := directory.CreateUser(ctx, identity.User{
		ID:           uuid.New(),
		Email:        address,
		PasswordHash: "$argon2id$v=19$m=1024,t=1,p=1$c2FsdA$a2V5",
		Status:       identity.StatusActive,
		Locale:       "en-US",
		Timezone:     "Europe/Berlin",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Every column the service reads has to come back, including the two the request body
	// supplied and the status default.
	if created.Email != address || created.Locale != "en-US" || created.Timezone != "Europe/Berlin" {
		t.Errorf("the record changed in storage: %+v", created)
	}
	if created.Status != identity.StatusActive {
		t.Errorf("status = %q", created.Status)
	}

	byEmail, err := directory.FindUserByEmail(ctx, address)
	if err != nil {
		t.Fatalf("find by address: %v", err)
	}
	if byEmail.ID != created.ID {
		t.Errorf("lookup by address returned %s, expected %s", byEmail.ID, created.ID)
	}

	byID, err := directory.FindUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if byID.Email != address {
		t.Errorf("lookup by id returned %q", byID.Email)
	}

	// A raised cost is applied by replacing the stored hash on the next successful sign-in,
	// so the update has to actually take effect and be readable back.
	replacement := "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$a2V5"
	if err := directory.UpdateUserPasswordHash(ctx, created.ID, replacement); err != nil {
		t.Fatalf("update hash: %v", err)
	}
	if err := directory.RecordUserLogin(ctx, created.ID); err != nil {
		t.Fatalf("record login: %v", err)
	}

	afterUpdate, err := directory.FindUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("find after update: %v", err)
	}
	if afterUpdate.PasswordHash != replacement {
		t.Error("the replaced hash was not stored")
	}
}

func TestUserStatementsReportAbsenceDistinctly(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	if _, err := directory.FindUserByEmail(ctx, uniqueAddress()); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("a missing address returned %v", err)
	}
	if _, err := directory.FindUserByID(ctx, uuid.New()); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("a missing id returned %v", err)
	}

	// The administrative lookups go through the same adapter and must not report the user
	// space's error: the service distinguishes the two, and a shared error would make that
	// impossible.
	if _, err := directory.FindAdminByEmail(ctx, uniqueAddress()); !errors.Is(err, identity.ErrAdminNotFound) {
		t.Errorf("a missing administrator returned %v", err)
	}
	if _, err := directory.FindAdminByID(ctx, uuid.New()); !errors.Is(err, identity.ErrAdminNotFound) {
		t.Errorf("a missing administrator id returned %v", err)
	}
}

func TestCreateUserReportsARealUniqueViolationAsAConflict(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	address := uniqueAddress()
	purge(t, pool, address)

	user := identity.User{
		ID: uuid.New(), Email: address, PasswordHash: "hash",
		Status: identity.StatusActive, Locale: "zh-CN", Timezone: "UTC",
	}
	if _, err := directory.CreateUser(ctx, user); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// The same address with a different identifier, so the conflict is the address's UNIQUE
	// constraint rather than the primary key's. This is the mapping the service relies on to
	// tell a caller their address is taken, and errors_test.go can only assert it against a
	// synthetic SQLSTATE.
	user.ID = uuid.New()
	if _, err := directory.CreateUser(ctx, user); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("a duplicate address returned %v", err)
	}
}

func TestCreateUserRejectsAStatusTheConstraintDoesNotAllow(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	address := uniqueAddress()
	purge(t, pool, address)

	_, err := directory.CreateUser(ctx, identity.User{
		ID: uuid.New(), Email: address, PasswordHash: "hash",
		Status: "deactivated", Locale: "zh-CN", Timezone: "UTC",
	})

	// A CHECK violation is not a conflict with the caller's input and must not be reported
	// as one: the service would tell a user to try a different address for a problem that
	// has nothing to do with the address.
	if err == nil {
		t.Fatal("a status outside the constraint was accepted")
	}
	if errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("a CHECK violation was reported as an address conflict: %v", err)
	}
}

func TestCreateUserRejectsALocaleTheConstraintDoesNotAllow(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	address := uniqueAddress()
	purge(t, pool, address)

	// docs/13 defines exactly two locales, and the constraint is what makes a third
	// impossible rather than merely unrendered.
	_, err := directory.CreateUser(ctx, identity.User{
		ID: uuid.New(), Email: address, PasswordHash: "hash",
		Status: identity.StatusActive, Locale: "fr-FR", Timezone: "UTC",
	})
	if err == nil {
		t.Fatal("an unsupported locale was accepted")
	}
	if errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("the locale constraint was reported as an address conflict: %v", err)
	}
}

func TestCreateUserRefusesAnAddressThatIsNotNormalized(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	address := uniqueAddress()
	purge(t, pool, address)

	// The application normalises before writing, so this never happens through the service.
	// It is asserted anyway, because the plain UNIQUE constraint on the column is only
	// sufficient if every stored address is already normalised: "Ada@example.com" and
	// "ada@example.com" would otherwise be two accounts, and the sign-in path — which
	// normalises — could reach only one of them.
	//
	// The constraint is what makes that a property of the schema rather than a rule each
	// future writer has to remember. A profile-edit screen written in a later phase that
	// forgot to normalise would otherwise create an account its owner could not sign in to,
	// silently, with no error until they tried.
	_, err := directory.CreateUser(ctx, identity.User{
		ID: uuid.New(), Email: "Ada." + address, PasswordHash: "hash",
		Status: identity.StatusActive, Locale: "zh-CN", Timezone: "UTC",
	})
	if err == nil {
		t.Fatal("an unnormalised address was stored")
	}
	if errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("the constraint was reported as an address conflict: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE lower(email) = lower($1)", address).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows were written despite the refusal", count)
	}
}

// createAdmin inserts an administrator and returns its identifier.
//
// Written as SQL rather than through the adapter because there is no administrative
// creation path yet — that arrives with the phase that owns admins management — and a test
// helper is not the place to invent one. It also keeps the roles assignment in the test,
// where the permission assertions can name it.
func createAdmin(t *testing.T, pool *pgxpool.Pool, roles ...string) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	address := uniqueAddress()
	purge(t, pool, address)

	id := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO admins (id, email, password_hash, status, display_name)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, address, "hash", identity.StatusActive, "Test Administrator")
	if err != nil {
		t.Fatalf("insert administrator: %v", err)
	}

	for _, role := range roles {
		tag, err := pool.Exec(ctx,
			`INSERT INTO admin_roles (admin_id, role_id)
			 SELECT $1, r.id FROM roles r WHERE r.key = $2`,
			id, role)
		if err != nil {
			t.Fatalf("grant role %q: %v", role, err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("role %q does not exist; the RBAC seed did not run", role)
		}
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM admin_roles WHERE admin_id = $1", id)
		_, _ = pool.Exec(context.Background(), "DELETE FROM admins WHERE id = $1", id)
	})
	return id
}

func TestPermissionsForAdminResolvesTheSeededRoles(t *testing.T) {
	pool := newPool(t)
	directory := NewDirectory(pool)
	ctx := context.Background()

	var total int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM permissions").Scan(&total); err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	// docs/15 fixed the original 27 permission keys; later phases extend the
	// vocabulary by migration (0005 added the subscription keys), which is how a
	// CHECK-guarded vocabulary is allowed to grow. The floor is asserted so the
	// test cannot pass on a seed that lost rows.
	if total < 27 {
		t.Fatalf("the permissions table holds %d rows, expected at least the 27 docs/15 "+
			"fixed; the seed did not run or was changed", total)
	}

	superAdmin := createAdmin(t, pool, "super_admin")
	permissions, err := directory.PermissionsForAdmin(ctx, superAdmin)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(permissions) != total {
		t.Errorf("super_admin holds %d permissions, expected all %d", len(permissions), total)
	}

	// read_only is derived from the naming convention rather than listed, so it is the role
	// that proves a permission added by a later phase is picked up without editing the seed.
	readOnly := createAdmin(t, pool, "read_only")
	readPermissions, err := directory.PermissionsForAdmin(ctx, readOnly)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(readPermissions) == 0 {
		t.Fatal("read_only holds nothing")
	}
	for _, key := range readPermissions {
		if len(key) < 5 || key[len(key)-5:] != ".read" {
			t.Errorf("read_only holds %q, which does not end in .read", key)
		}
	}
	if len(readPermissions) >= total {
		t.Errorf("read_only holds %d of %d permissions; it is not a restricted role",
			len(readPermissions), total)
	}

	// An administrator with no roles has no permissions. Reported as an empty set rather
	// than as an error, because it is a legitimate state rather than a failure.
	noRoles := createAdmin(t, pool)
	none, err := directory.PermissionsForAdmin(ctx, noRoles)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an administrator with no roles holds %v", none)
	}

	// Several roles granted at once resolve to their union, which is what makes a second
	// role additive rather than a replacement.
	support := createAdmin(t, pool, "support", "finance")
	union, err := directory.PermissionsForAdmin(ctx, support)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	supportOnly := createAdmin(t, pool, "support")
	supportPermissions, err := directory.PermissionsForAdmin(ctx, supportOnly)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	financeOnly := createAdmin(t, pool, "finance")
	financePermissions, err := directory.PermissionsForAdmin(ctx, financeOnly)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(union) <= len(supportPermissions) || len(union) <= len(financePermissions) {
		t.Errorf("the union of support and finance is %d permissions, which is not larger than "+
			"either role alone (%d, %d)", len(union), len(supportPermissions), len(financePermissions))
	}
}

func TestAuditEventsAreRecordedWithTheirColumns(t *testing.T) {
	pool := newPool(t)
	recorder := NewRecorder(pool)
	ctx := context.Background()

	actor := createAdmin(t, pool, "read_only")

	event := audit.Event{
		ActorType:    audit.ActorAdmin,
		ActorID:      actor,
		Action:       "admin.login.succeeded",
		ResourceType: "admin",
		ResourceID:   actor,
		Details:      map[string]string{"attempted": "r***@example.com"},
		Context: audit.Context{
			IP:        "203.0.113.7",
			UserAgent: "Go-http-client/1.1",
			RequestID: "req-" + uuid.NewString(),
			TraceID:   "trace-" + uuid.NewString(),
		},
	}
	if err := recorder.Record(ctx, event); err != nil {
		t.Fatalf("record: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM audit_events WHERE request_id = $1", event.Context.RequestID)
	})

	// Read back through SQL rather than through a statement the recorder owns, so the
	// assertion is about what is actually in the table — including the columns nothing in
	// Phase 1 reads yet.
	var (
		actorType, action, resourceType string
		actorID, resourceID             uuid.UUID
		ip                              string
		userAgent, requestID, traceID   *string
		after                           map[string]string
		beforeIsNull                    bool
	)
	err := pool.QueryRow(ctx,
		`SELECT actor_type, actor_id, action, resource_type, resource_id,
		        host(ip_address), user_agent, request_id, trace_id, after_data, before_data IS NULL
		 FROM audit_events WHERE request_id = $1`,
		event.Context.RequestID,
	).Scan(&actorType, &actorID, &action, &resourceType, &resourceID,
		&ip, &userAgent, &requestID, &traceID, &after, &beforeIsNull)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// The source information is a column rather than a JSON entry, so an operator can filter
	// the trail by address without depending on a key being spelled the same way everywhere.
	if ip != "203.0.113.7" {
		t.Errorf("ip_address = %q", ip)
	}
	if actorType != audit.ActorAdmin || actorID != actor || resourceID != actor {
		t.Errorf("the entry does not name the acting administrator: %s %s %s", actorType, actorID, resourceID)
	}
	if action != "admin.login.succeeded" || resourceType != "admin" {
		t.Errorf("action/resource = %s/%s", action, resourceType)
	}
	if userAgent == nil || requestID == nil || traceID == nil {
		t.Errorf("the correlation columns are null: ua=%v req=%v trace=%v", userAgent, requestID, traceID)
	}
	if after["attempted"] != "r***@example.com" {
		t.Errorf("after_data = %v", after)
	}
	// before_data stays NULL in Phase 1: nothing audited here changes an existing value, and
	// the column is for the phases that adjust a balance or a subscription.
	if !beforeIsNull {
		t.Error("before_data was written; Phase 1 audits nothing with a previous state")
	}
}

func TestAuditRecorderWritesAnEmptyDetailsObject(t *testing.T) {
	pool := newPool(t)
	recorder := NewRecorder(pool)
	ctx := context.Background()

	// A system action: the reconciler and the outbox relay will write these, and they have no
	// actor. The actor column is nullable for exactly this case.
	err := recorder.Record(ctx, audit.Event{
		ActorType:    audit.ActorSystem,
		Action:       "system.probe",
		ResourceType: "platform",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM audit_events WHERE actor_type = 'system' AND action = 'system.probe'")
	})

	// Found by action, since a system entry has no request id to key on.
	var (
		actorIsNull  bool
		afterNotNull bool
	)
	err = pool.QueryRow(ctx,
		`SELECT actor_id IS NULL, after_data IS NOT NULL
		 FROM audit_events WHERE actor_type = 'system' AND action = 'system.probe'
		 ORDER BY created_at DESC LIMIT 1`,
	).Scan(&actorIsNull, &afterNotNull)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !actorIsNull {
		t.Error("a system action recorded an actor; the column is nullable for this case")
	}
	// An empty object rather than NULL, so a reader always has something to query.
	if !afterNotNull {
		t.Error("after_data is NULL; an empty object is written instead")
	}
}

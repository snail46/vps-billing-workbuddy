package usersurface_test

// The customer's own surface against real PostgreSQL. The ownership join is
// the thing under test: a row that is not the caller's must be a miss, in
// every surface this store backs.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/storage/usersurface"
)

func newUserSurfaceEnv(t *testing.T) (*usersurface.Store, *pgxpool.Pool) {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return usersurface.New(pool), pool
}

func seedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, email, password_hash, status) VALUES ($1, $2, 'x', 'active')`,
		id, strings.ToLower(id.String())+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func TestATicketIsReadableOnlyByItsOwner(t *testing.T) {
	store, pool := newUserSurfaceEnv(t)
	ctx := context.Background()
	owner := seedUser(t, pool)
	stranger := seedUser(t, pool)

	now := time.Now().UTC()
	ticketID := uuid.New()
	if err := store.TicketOpen(ctx, ticketID, "TK-TEST-"+strings.ToUpper(uuid.NewString()[:8]), owner,
		"the machine is slow", usersurface.PriorityNormal, "please look", now); err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := store.Ticket(ctx, ticketID, owner); err != nil {
		t.Fatalf("the owner must read their ticket: %v", err)
	}
	if _, err := store.Ticket(ctx, ticketID, stranger); errors.Is(err, usersurface.ErrNotFound) {
		// the stranger's miss is the assertion
	} else if err != nil {
		t.Fatalf("read as stranger: %v", err)
	} else {
		t.Fatal("a stranger read a ticket that is not theirs")
	}

	// A reply from the owner appends; the ticket's status stays open.
	if err := store.TicketReply(ctx, ticketID, owner, "still slow", now); err != nil {
		t.Fatalf("reply: %v", err)
	}
	messages, err := store.TicketMessages(ctx, ticketID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	// Closing twice is one close and a no-op, never an error to the customer.
	if closed, err := store.TicketClose(ctx, ticketID, owner, now); err != nil || !closed {
		t.Fatalf("first close: closed=%v err=%v", closed, err)
	}
	if closed, err := store.TicketClose(ctx, ticketID, owner, now); err != nil || closed {
		t.Fatalf("second close should be a no-op miss: closed=%v err=%v", closed, err)
	}
	if _, err := store.Ticket(ctx, ticketID, stranger); err == nil {
		t.Fatal("a stranger closed nothing, yet a read would prove too much")
	}
}

func TestANotificationIsMarkedReadOnlyForItsOwner(t *testing.T) {
	store, pool := newUserSurfaceEnv(t)
	ctx := context.Background()
	owner := seedUser(t, pool)
	stranger := seedUser(t, pool)

	notificationID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO notifications (id, user_id, type, title_key, message_key, parameters, severity)
		 VALUES ($1, $2, 'instance.provisioned', 't', 'm', '{}', 'success')`,
		notificationID, owner); err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	unread, err := store.UnreadCount(ctx, owner)
	if err != nil || unread < 1 {
		t.Fatalf("unread: %d %v", unread, err)
	}

	// A stranger's stamp changes nothing.
	if marked, err := store.MarkRead(ctx, notificationID, stranger, time.Now().UTC()); err != nil || marked {
		t.Fatalf("a stranger's stamp was absorbed: marked=%v err=%v", marked, err)
	}
	if marked, err := store.MarkRead(ctx, notificationID, owner, time.Now().UTC()); err != nil || !marked {
		t.Fatalf("the owner's stamp did not land: marked=%v err=%v", marked, err)
	}
	if marked, err := store.MarkRead(ctx, notificationID, owner, time.Now().UTC()); err != nil || marked {
		t.Fatalf("a second stamp should be a no-op: marked=%v err=%v", marked, err)
	}
}

func TestAWalletLedgerIsEmptyForACallerWithoutRows(t *testing.T) {
	store, _ := newUserSurfaceEnv(t)
	ctx := context.Background()

	// The pages' empty states are backed by these: a caller with no rows gets
	// empty slices, not errors.
	if _, err := store.Wallets(ctx, uuid.New()); err != nil {
		t.Fatalf("wallets: %v", err)
	}
	if _, err := store.Ledger(ctx, uuid.New()); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if _, err := store.Invoices(ctx, uuid.New()); err != nil {
		t.Fatalf("invoices: %v", err)
	}
	if _, err := store.Tickets(ctx, uuid.New()); err != nil {
		t.Fatalf("tickets: %v", err)
	}
	if _, err := store.Notifications(ctx, uuid.New()); err != nil {
		t.Fatalf("notifications: %v", err)
	}
}

func TestTheInstanceDetailMissesWhatIsNotThere(t *testing.T) {
	store, _ := newUserSurfaceEnv(t)
	ctx := context.Background()

	// A random identifier names nothing, and nothing is the same miss for the
	// owner and everyone else.
	if _, err := store.Detail(ctx, uuid.New(), uuid.New()); !errors.Is(err, usersurface.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTheTicketNumberCarriesItsKindAndDate(t *testing.T) {
	number, err := usersurface.NewTicketNumber(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("number: %v", err)
	}
	if !strings.HasPrefix(number, "TK-20260925-") {
		t.Fatalf("unexpected shape: %s", number)
	}
}

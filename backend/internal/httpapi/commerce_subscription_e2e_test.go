package httpapi_test

// The subscription lifecycle end to end, against real PostgreSQL and Redis.
//
// The machine is calendar-driven, and a calendar is exactly what a fake cannot
// prove: the sweep is handed `now` as an argument, so these tests sweep into
// next month, past grace, and past expiry without waiting for any of it — and
// the property under test, that two concurrent sweeps renew one subscription
// exactly once, is a property of the conditional updates and the unique index
// rather than of the code around them (ADR-006 §6).

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/ledger"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
	commercestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/commerce"
)

// subscriptionServiceFor builds the service the tests drive the sweep through.
// It shares the fixture's store and gateway, so a subscription the HTTP surface
// created is one this service can sweep.
func subscriptionServiceFor(t *testing.T, e *e2eEnv) *commerce.Service {
	t.Helper()
	svc, err := commerce.NewService(commerce.Deps{
		Store:    commercestore.New(e.pool),
		Gateways: commerce.Gateways{e.gateway.Name(): e.gateway},
	})
	if err != nil {
		t.Fatalf("build the commerce service: %v", err)
	}
	return svc
}

// topUpWallet puts spendable balance behind a customer, by posting the movement
// the ledger would record for money received and credited to the wallet. The
// projection follows the entries in the same transaction (ADR-005), so the
// balance the spend gate reads is the ledger's own answer.
func topUpWallet(t *testing.T, e *e2eEnv, userID uuid.UUID, amount int64, currency string) {
	t.Helper()
	poster := commercestore.New(e.pool).Poster()
	err := poster.Post(context.Background(), ledger.Transaction{
		ID:          uuid.New(),
		Type:        ledger.TypeAdjustment,
		Description: "test top-up",
		Entries: []ledger.Entry{
			{
				AccountType: ledger.AccountGatewayClearing,
				AccountID:   ledger.AccountIDGatewayClearing,
				Direction:   ledger.DirectionDebit,
				Amount:      money.Money{AmountMinor: amount, Currency: money.Currency(currency)},
			},
			{
				AccountType: ledger.AccountUserWallet,
				AccountID:   userID,
				Direction:   ledger.DirectionCredit,
				Amount:      money.Money{AmountMinor: amount, Currency: money.Currency(currency)},
			},
		},
	})
	if err != nil {
		t.Fatalf("top up the wallet: %v", err)
	}
}

// walletBalance reads the projection back. The ledger is the truth; this is what
// the spend gate will see.
func walletBalance(t *testing.T, e *e2eEnv, userID uuid.UUID, currency string) int64 {
	t.Helper()
	var balance int64
	if err := e.pool.QueryRow(context.Background(),
		"SELECT available_balance_minor FROM wallets WHERE user_id = $1 AND currency = $2",
		userID, currency).Scan(&balance); err != nil {
		t.Fatalf("read the wallet: %v", err)
	}
	return balance
}

// firstSubscriptionID lists through the HTTP surface and returns the customer's
// only subscription, failing if the count is different.
func firstSubscriptionID(t *testing.T, e *e2eEnv, cookie *http.Cookie, token string) string {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/subscriptions", "", cookie, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("list subscriptions: %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Subscriptions []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the list: %v", err)
	}
	if len(body.Data.Subscriptions) != 1 {
		t.Fatalf("the customer holds %d subscriptions, expected 1", len(body.Data.Subscriptions))
	}
	return body.Data.Subscriptions[0].ID
}

// subscriptionStatus reads one subscription's status straight from the row, so
// the assertions below check the machine's record rather than a payload's echo.
func subscriptionStatus(t *testing.T, e *e2eEnv, id string) (string, *time.Time, time.Time) {
	t.Helper()
	var status string
	var endedAt, periodEnd *time.Time
	if err := e.pool.QueryRow(context.Background(),
		"SELECT status, ended_at, current_period_end FROM subscriptions WHERE id = $1",
		id).Scan(&status, &endedAt, &periodEnd); err != nil {
		t.Fatalf("read the subscription: %v", err)
	}
	if periodEnd == nil {
		t.Fatal("the subscription has no period end; only rows this code did not write look like that")
	}
	return status, endedAt, *periodEnd
}

// countInvoicesForSubscription counts the renewal bills a subscription has ever
// had, which is how "the sweep opened one, and only one" is asserted.
func countInvoicesForSubscription(t *testing.T, e *e2eEnv, id string) int {
	return count(t, e, "SELECT count(*) FROM invoices WHERE subscription_id = $1", id)
}

func TestSubscriptionIsBornFromItsOrder(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)

	// The subscription exists, and it is active: born settled, per ADR-006 §2.
	id := firstSubscriptionID(t, e, cookie, token)
	status, _, periodEnd := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionActive {
		t.Fatalf("a settled order produced a %q subscription", status)
	}

	// The period is one calendar month, starting now. A fixed 30 days would drift
	// the anniversary; the assertion is the window a month can span.
	start := time.Now().UTC()
	if periodEnd.Before(start.AddDate(0, 1, -2)) ||
		periodEnd.After(start.AddDate(0, 1, 2)) {
		t.Errorf("the first period ends at %v, which is not one calendar month out", periodEnd)
	}

	// The activation is an event, not a row change alone: provisioning (Phase 6)
	// subscribes to it rather than polling the table.
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'subscription.activated.v1' AND aggregate_id = $1`, id); got != 1 {
		t.Errorf("the activation wrote %d events, expected 1", got)
	}
}

func TestRenewalPaysFromTheWalletAndExtendsThePeriod(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)
	user := firstSubscriptionOwner(t, e, cookie, token)

	// Balance enough for exactly one renewal.
	topUpWallet(t, e, user, testPlanPrice, testCurrency)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)

	_, _, periodEnd := subscriptionStatus(t, e, id)
	svc := subscriptionServiceFor(t, e)
	result, err := svc.Sweep(context.Background(), periodEnd.Add(time.Second))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Renewed != 1 {
		t.Fatalf("the sweep renewed %d subscriptions, expected 1 (%+v)", result.Renewed, result)
	}

	// The period moved forward by one cycle from where it was, not from the
	// sweep's `now`: a late renewal pays for the service it missed.
	status, _, newPeriodEnd := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionActive {
		t.Errorf("a paid renewal left the subscription %q", status)
	}
	want := periodEnd.AddDate(0, 1, 0)
	if !newPeriodEnd.Equal(want) {
		t.Errorf("the period ends at %v, expected %v", newPeriodEnd, want)
	}

	// The money: out of the wallet, into revenue, in one ledger transaction.
	if got := walletBalance(t, e, user, testCurrency); got != 0 {
		t.Errorf("the balance is %d after the renewal, expected 0", got)
	}
	if got := count(t, e, `SELECT count(*) FROM ledger_transactions
		WHERE reference_type = 'invoice'
		  AND reference_id IN (SELECT id FROM invoices WHERE subscription_id = $1)`, id); got != 1 {
		t.Errorf("the renewal wrote %d ledger transactions, expected 1", got)
	}
	var debit, credit int64
	if err := e.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_minor END), 0),
		       COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_minor END), 0)
		FROM ledger_entries WHERE transaction_id IN (
			SELECT id FROM ledger_transactions WHERE reference_type = 'invoice'
			AND reference_id IN (SELECT id FROM invoices WHERE subscription_id = $1))`,
		id).Scan(&debit, &credit); err != nil {
		t.Fatalf("read the entries: %v", err)
	}
	if debit != testPlanPrice || credit != testPlanPrice {
		t.Errorf("the renewal movement is %d debit against %d credit for %d",
			debit, credit, testPlanPrice)
	}

	// One invoice, one event, and the projection agrees with the entries it
	// summarises.
	if got := countInvoicesForSubscription(t, e, id); got != 1 {
		t.Errorf("the subscription has %d invoices, expected 1", got)
	}
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'subscription.renewed.v1' AND aggregate_id = $1`, id); got != 1 {
		t.Errorf("the renewal wrote %d events, expected 1", got)
	}
	if got := count(t, e, `SELECT count(*) FROM invoices
		WHERE subscription_id = $1 AND status = 'paid'`, id); got != 1 {
		t.Errorf("%d renewal invoices are paid, expected 1", got)
	}
}

func TestUnpaidRenewalWalksPastDueSuspendedExpired(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)
	// No top-up: the wallet cannot back the renewal, which is the whole point.

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)

	_, _, periodEnd := subscriptionStatus(t, e, id)
	svc := subscriptionServiceFor(t, e)
	ctx := context.Background()

	// At the period's end the sweep opens the bill and marks the row past due,
	// with a grace deadline 72 hours out. The work list is global — the database
	// is shared with every other test — so the assertions below are about THIS
	// subscription's row, events and bills, never about the sweep's tally.
	_, err := svc.Sweep(ctx, periodEnd.Add(time.Second))
	if err != nil {
		t.Fatalf("sweep at the period's end: %v", err)
	}
	status, _, _ := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionPastDue {
		t.Fatalf("an unpaid renewal left the subscription %q", status)
	}
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'subscription.past_due.v1' AND aggregate_id = $1`, id); got != 1 {
		t.Errorf("the past-due transition wrote %d events, expected 1", got)
	}

	// Sweeping again inside grace changes nothing but opens no second bill.
	_, err = svc.Sweep(ctx, periodEnd.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sweep inside grace: %v", err)
	}
	if status, _, _ = subscriptionStatus(t, e, id); status != commerce.SubscriptionPastDue {
		t.Errorf("a sweep inside grace moved the subscription to %q", status)
	}
	if countInvoicesForSubscription(t, e, id) != 1 {
		t.Errorf("a sweep inside grace opened a second bill")
	}

	// Past grace: suspended, with an expiry deadline.
	_, err = svc.Sweep(ctx, periodEnd.Add(73*time.Hour))
	if err != nil {
		t.Fatalf("sweep past grace: %v", err)
	}
	status, _, _ = subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionSuspended {
		t.Fatalf("a subscription past grace is %q", status)
	}

	// Sweeping inside the suspension window changes nothing: the row's expiry
	// deadline is still ahead, so the machine leaves it suspended.
	_, err = svc.Sweep(ctx, periodEnd.Add(73*time.Hour+24*time.Hour))
	if err != nil {
		t.Fatalf("sweep inside the suspension window: %v", err)
	}
	if status, _, _ = subscriptionStatus(t, e, id); status != commerce.SubscriptionSuspended {
		t.Errorf("a sweep inside the suspension window moved the subscription to %q", status)
	}
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'subscription.expired.v1' AND aggregate_id = $1`, id); got != 0 {
		t.Errorf("the suspension window wrote %d expiry events before the deadline", got)
	}

	// Past the expiry deadline: expired, and the machine stops.
	_, err = svc.Sweep(ctx, periodEnd.Add(73*time.Hour+14*24*time.Hour+time.Hour))
	if err != nil {
		t.Fatalf("sweep past expiry: %v", err)
	}
	status, endedAt, _ := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionExpired || endedAt == nil {
		t.Fatalf("an expired subscription is %q with ended_at %v", status, endedAt)
	}
}

func TestCancelAtPeriodEndIsHonouredBeforeBilling(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)

	if rec := e.do(t, http.MethodPost, "/api/v1/subscriptions/"+id+"/cancel",
		"", cookie, token); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d (%s)", rec.Code, rec.Body.String())
	}

	_, _, periodEnd := subscriptionStatus(t, e, id)
	svc := subscriptionServiceFor(t, e)
	result, err := svc.Sweep(context.Background(), periodEnd.Add(time.Second))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Cancelled != 1 {
		t.Fatalf("the sweep cancelled %d, expected 1 (%+v)", result.Cancelled, result)
	}
	status, endedAt, _ := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionCancelled || endedAt == nil {
		t.Fatalf("a cancelled subscription is %q with ended_at %v", status, endedAt)
	}

	// The cancellation is honoured before the billing: no invoice was ever
	// opened, because a cancelled subscription must not generate a bill.
	if got := countInvoicesForSubscription(t, e, id); got != 0 {
		t.Errorf("a cancelled subscription generated %d invoices", got)
	}
}

func TestConcurrentSweepsRenewExactlyOnce(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)
	user := firstSubscriptionOwner(t, e, cookie, token)

	// Balance enough for exactly one renewal: a second one must be impossible.
	topUpWallet(t, e, user, testPlanPrice, testCurrency)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)

	_, _, periodEnd := subscriptionStatus(t, e, id)
	svc := subscriptionServiceFor(t, e)
	when := periodEnd.Add(time.Second)

	var wg sync.WaitGroup
	var renewedTotal int64
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := svc.Sweep(context.Background(), when)
			if err != nil {
				errs <- err
				return
			}
			// Each sweep's own result is counted atomically: the sweeps run
			// concurrently, and the tally is what "exactly once" is read from.
			atomic.AddInt64(&renewedTotal, int64(result.Renewed))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent sweep failed: %v", err)
	}

	// The property: one renewal, wherever it started. The unique index on open
	// renewal invoices arbitrated the bills, the invoice's conditional transition
	// arbitrated the payment, and the balance gate made a second spend impossible.
	if renewedTotal != 1 {
		t.Errorf("%d of two concurrent sweeps renewed, expected exactly 1", renewedTotal)
	}
	if got := walletBalance(t, e, user, testCurrency); got != 0 {
		t.Errorf("the balance is %d; a renewal was paid twice", got)
	}
	if got := count(t, e, `SELECT count(*) FROM ledger_transactions
		WHERE reference_type = 'invoice'
		  AND reference_id IN (SELECT id FROM invoices WHERE subscription_id = $1)`, id); got != 1 {
		t.Errorf("the ledger holds %d renewal movements, expected 1", got)
	}
	status, _, newPeriodEnd := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionActive || !newPeriodEnd.Equal(periodEnd.AddDate(0, 1, 0)) {
		t.Errorf("the period is %q ending %v; it should be active ending %v",
			status, newPeriodEnd, periodEnd.AddDate(0, 1, 0))
	}
}

func TestRenewNowPaysADueRenewal(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)
	user := firstSubscriptionOwner(t, e, cookie, token)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)
	_, _, periodEnd := subscriptionStatus(t, e, id)

	// Before the period ends there is nothing to renew, and the endpoint says so
	// rather than taking money for service already paid for.
	if rec := e.do(t, http.MethodPost, "/api/v1/subscriptions/"+id+"/renew",
		"", cookie, token); rec.Code != http.StatusConflict {
		t.Fatalf("an early renewal answered %d (%s)", rec.Code, rec.Body.String())
	}

	// The sweep runs with an empty wallet: the bill opens, the row goes past due.
	svc := subscriptionServiceFor(t, e)
	if _, err := svc.Sweep(context.Background(), periodEnd.Add(time.Second)); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// The customer tops up and pays. The renewal is the same transaction the
	// sweep would have run, started from the customer's side.
	topUpWallet(t, e, user, testPlanPrice, testCurrency)
	rec := e.do(t, http.MethodPost, "/api/v1/subscriptions/"+id+"/renew",
		"", cookie, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("renew: %d (%s)", rec.Code, rec.Body.String())
	}
	status, _, newPeriodEnd := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionActive || !newPeriodEnd.Equal(periodEnd.AddDate(0, 1, 0)) {
		t.Errorf("a manual renewal left the subscription %q ending %v", status, newPeriodEnd)
	}

	// Renewing again is refused: the new period has not ended.
	if rec := e.do(t, http.MethodPost, "/api/v1/subscriptions/"+id+"/renew",
		"", cookie, token); rec.Code != http.StatusConflict {
		t.Errorf("a second renewal answered %d, expected a conflict", rec.Code)
	}
}

func TestTerminateIsTheAdministrativeHand(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	deliverCallback(t, e, order)
	id := firstSubscriptionID(t, e, cookie, token)

	svc := subscriptionServiceFor(t, e)
	if err := svc.Terminate(context.Background(), uuid.MustParse(id), time.Now()); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	status, endedAt, _ := subscriptionStatus(t, e, id)
	if status != commerce.SubscriptionTerminated || endedAt == nil {
		t.Fatalf("a terminated subscription is %q with ended_at %v", status, endedAt)
	}
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'subscription.terminated.v1' AND aggregate_id = $1`, id); got != 1 {
		t.Errorf("the termination wrote %d events, expected 1", got)
	}

	// Terminal means terminal: a second termination is refused, not idempotent,
	// because the first one's event is the record and a second would be a lie.
	if err := svc.Terminate(context.Background(), uuid.MustParse(id), time.Now()); err == nil {
		t.Fatal("a terminated subscription was terminated again")
	}
}

// deliverCallback posts the payment callback for an order placed through the
// HTTP surface, signed the way the gateway signs it, so the subscription is born
// from a real settlement.
func deliverCallback(t *testing.T, e *e2eEnv, order placedOrder) {
	t.Helper()
	if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}
}

// firstSubscriptionOwner reads the signed-in customer's identifier out of the
// users table by their session's subject. The fixture signs up through the API,
// so the address is the lookup; the identifier is what the ledger entries need.
func firstSubscriptionOwner(t *testing.T, e *e2eEnv, cookie *http.Cookie, token string) uuid.UUID {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the profile: %v", err)
	}
	id, err := uuid.Parse(body.Data.User.ID)
	if err != nil {
		t.Fatalf("the profile carries no identifier: %v", err)
	}
	return id
}

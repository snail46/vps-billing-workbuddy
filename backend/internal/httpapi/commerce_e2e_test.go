package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// The commercial surface end to end, and with it the Gate of Phase 2:
//
//	同一支付回调重复 100 次只入账一次
//
// Everything here runs against real PostgreSQL and Redis, because the property under
// test — that a hundred deliveries of one callback write one settlement — is a property
// of the database's conditional update and unique index, and neither can be established
// against a fake. A settlement gated on "read, check, write" passes the sequential round
// and fails the concurrent one, which is why both are here.

const (
	testGatewayName  = "fake"
	testCurrency     = "CNY"
	testPlanPrice    = 9900
	testPlanQuantity = 1
)

// catalogEntry is a product and its plans, inserted for one test and removed after it.
type catalogEntry struct {
	productID uuid.UUID
	planID    uuid.UUID
}

// seedCatalog inserts a product with an active and a draft plan.
//
// The draft one is there because the catalogue must not sell it, and a test that only
// seeded active plans would pass with a handler that sold everything.
func seedCatalog(t *testing.T, e *e2eEnv) catalogEntry {
	t.Helper()
	ctx := context.Background()

	entry := catalogEntry{productID: uuid.New(), planID: uuid.New()}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO products (id, slug, name_i18n, status)
		VALUES ($1, $2, $3::jsonb, 'active')`,
		entry.productID, "e2e-product-"+uuid.NewString()[:8],
		`{"zh-CN":"测试产品","en-US":"Test product"}`); err != nil {
		t.Fatalf("insert the product: %v", err)
	}

	// The slug is unique per product, so it can be stable across runs.
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO plans (id, product_id, slug, name_i18n, status, cpu_cores, memory_mb, disk_gb,
		                   virtualization, billing_cycle, price_minor, currency)
		VALUES ($1, $2, 'standard', $3::jsonb, 'active', '2', 4096, 80, 'kvm', 'monthly', $4, $5)`,
		entry.planID, entry.productID, `{"zh-CN":"标准型","en-US":"Standard"}`,
		testPlanPrice, testCurrency); err != nil {
		t.Fatalf("insert the active plan: %v", err)
	}

	_, err := e.pool.Exec(ctx, `
		INSERT INTO plans (id, product_id, slug, name_i18n, status, cpu_cores, memory_mb, disk_gb,
		                   virtualization, billing_cycle, price_minor, currency)
		VALUES ($1, $2, 'draft', $3::jsonb, 'draft', '4', 8192, 160, 'kvm', 'monthly', 19900, $4)`,
		uuid.New(), entry.productID, `{"zh-CN":"草稿型","en-US":"Draft"}`, testCurrency)
	if err != nil {
		t.Fatalf("insert the draft plan: %v", err)
	}

	t.Cleanup(func() {
		// The plans go before the product; the orders and payments that reference them
		// belong to the cleanup of the test that created those rows.
		_, _ = e.pool.Exec(ctx, "DELETE FROM plans WHERE product_id = $1", entry.productID)
		_, _ = e.pool.Exec(ctx, "DELETE FROM products WHERE id = $1", entry.productID)
	})
	return entry
}

// signUp walks a customer through registration and sign-in, and returns their session.
//
// It is the realistic path rather than minting a session directly, which means the
// commerce tests also re-prove that an account created through the API can buy things.
func signUp(t *testing.T, e *e2eEnv) (uuid.UUID, *http.Cookie, string) {
	t.Helper()

	address := e2eAddress()
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", address)
	})

	if rec := e.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"`+address+`","password":"`+e.password+`","locale":"en-US"}`, nil, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d (%s)", rec.Code, rec.Body.String())
	}

	login := e.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+address+`","password":"`+e.password+`"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d (%s)", login.Code, login.Body.String())
	}

	cookie := sessionCookie(login, authmw.UserCookieName)
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	var body struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the sign-in: %v", err)
	}
	userID, err := uuid.Parse(body.Data.User.ID)
	if err != nil {
		t.Fatalf("the sign-in returned no user id: %v", err)
	}
	return userID, cookie, body.Data.CSRFToken
}

// placedOrder is an order with a payment started against it.
type placedOrder struct {
	orderID          string
	gatewayPaymentID string
	amountMinor      int64
	callback         []byte
	signature        string
}

// placeOrderAndPayment walks the customer to the point of paying.
func placeOrderAndPayment(t *testing.T, e *e2eEnv, entry catalogEntry, cookie *http.Cookie, token string) placedOrder {
	t.Helper()

	rec := e.do(t, http.MethodPost, "/api/v1/orders",
		fmt.Sprintf(`{"items":[{"plan_id":"%s","quantity":%d}],"discount_minor":0}`,
			entry.planID, testPlanQuantity),
		cookie, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create the order: %d (%s)", rec.Code, rec.Body.String())
	}

	var created struct {
		Data struct {
			Order struct {
				ID         string `json:"id"`
				TotalMinor int64  `json:"total_minor"`
			} `json:"order"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode the order: %v", err)
	}
	if created.Data.Order.TotalMinor != testPlanPrice*testPlanQuantity {
		t.Fatalf("total = %d", created.Data.Order.TotalMinor)
	}

	start := e.do(t, http.MethodPost, "/api/v1/orders/"+created.Data.Order.ID+"/payments",
		`{"gateway":"`+testGatewayName+`"}`, cookie, token)
	if start.Code != http.StatusCreated {
		t.Fatalf("start the payment: %d (%s)", start.Code, start.Body.String())
	}

	var started struct {
		Data struct {
			Payment struct {
				GatewayPaymentID string `json:"gateway_payment_id"`
				AmountMinor      int64  `json:"amount_minor"`
			} `json:"payment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode the payment: %v", err)
	}
	if started.Data.Payment.GatewayPaymentID == "" {
		t.Fatal("the gateway returned no payment identifier")
	}

	amount, err := money.New(started.Data.Payment.AmountMinor, testCurrency)
	if err != nil {
		t.Fatalf("build the amount: %v", err)
	}
	body, signature, err := e.gateway.Notify(started.Data.Payment.GatewayPaymentID, "succeeded", amount)
	if err != nil {
		t.Fatalf("build the callback: %v", err)
	}

	return placedOrder{
		orderID:          created.Data.Order.ID,
		gatewayPaymentID: started.Data.Payment.GatewayPaymentID,
		amountMinor:      started.Data.Payment.AmountMinor,
		callback:         body,
		signature:        signature,
	}
}

// count runs a scalar count query, because almost every assertion below is "exactly one
// of these exists".
func count(t *testing.T, e *e2eEnv, query string, args ...any) int {
	t.Helper()
	var total int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	return total
}

// assertSettledOnce states what one settlement is allowed to have written. It is the
// assertion the Gate rests on, and it is about the database rather than about the
// response, because the response only says what this caller saw.
func (e *e2eEnv) assertSettledOnce(t *testing.T, orderID string, amountMinor int64) {
	t.Helper()

	if got := count(t, e, "SELECT count(*) FROM orders WHERE id = $1 AND status = 'paid' AND paid_at IS NOT NULL", orderID); got != 1 {
		t.Errorf("the order was not paid exactly once")
	}
	if got := count(t, e, "SELECT count(*) FROM invoices WHERE order_id = $1 AND status = 'paid'", orderID); got != 1 {
		t.Errorf("the invoice was not paid exactly once")
	}
	if got := count(t, e, "SELECT count(*) FROM payments WHERE order_id = $1 AND status = 'succeeded'", orderID); got != 1 {
		t.Errorf("the payment was not succeeded exactly once")
	}
	if got := count(t, e, `SELECT count(*) FROM ledger_transactions WHERE type = 'payment_settlement'
		AND reference_type = 'payment'
		AND reference_id IN (SELECT id FROM payments WHERE order_id = $1)`, orderID); got != 1 {
		t.Errorf("the ledger holds %d settlement transactions for one payment", got)
	}
	if got := count(t, e, `SELECT count(*) FROM ledger_entries WHERE transaction_id IN
		(SELECT id FROM ledger_transactions WHERE reference_type = 'payment'
		 AND reference_id IN (SELECT id FROM payments WHERE order_id = $1))`, orderID); got != 2 {
		t.Errorf("the settlement wrote %d entries, expected a debit and a credit", got)
	}

	// The entries balance, which is what makes the settlement a real double entry rather
	// than a single-sided credit: the money came from the gateway and became revenue.
	var debit, credit int64
	if err := e.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_minor END), 0),
		       COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_minor END), 0)
		FROM ledger_entries
		WHERE transaction_id IN (SELECT id FROM ledger_transactions WHERE reference_type = 'payment'
		                         AND reference_id IN (SELECT id FROM payments WHERE order_id = $1))`,
		orderID).Scan(&debit, &credit); err != nil {
		t.Fatalf("read the entries: %v", err)
	}
	if debit != credit || debit != amountMinor {
		t.Errorf("the entries do not balance: %d debit against %d credit for %d", debit, credit, amountMinor)
	}

	if got := count(t, e, `SELECT count(*) FROM outbox_events WHERE event_type = 'payment.succeeded.v1'
		AND aggregate_id IN (SELECT id FROM payments WHERE order_id = $1)`, orderID); got != 1 {
		t.Errorf("the outbox holds %d events for one settlement", got)
	}
}

func TestCatalogListsOnlyActivePlans(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)

	rec := e.do(t, http.MethodGet, "/api/v1/products", "", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the catalogue answered %d (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Products []struct {
				ID    string `json:"id"`
				Slug  string `json:"slug"`
				Plans []struct {
					ID         string `json:"id"`
					PriceMinor int64  `json:"price_minor"`
					Currency   string `json:"currency"`
				} `json:"plans"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the response is not the envelope: %v", err)
	}

	// The seeded product is the only one this run created, but other rows may exist in a
	// shared database, so it is found rather than assumed to be the only one.
	var found bool
	for _, product := range body.Data.Products {
		if product.ID != entry.productID.String() {
			continue
		}
		found = true
		if len(product.Plans) != 1 {
			t.Fatalf("the catalogue offered %d plans, expected only the active one", len(product.Plans))
		}
		if product.Plans[0].ID != entry.planID.String() {
			t.Errorf("the catalogue offered plan %s", product.Plans[0].ID)
		}
		if product.Plans[0].PriceMinor != testPlanPrice || product.Plans[0].Currency != testCurrency {
			t.Errorf("price = %d %s", product.Plans[0].PriceMinor, product.Plans[0].Currency)
		}
	}
	if !found {
		t.Fatal("the seeded product is not in the catalogue")
	}
}

func TestOrderAndPaymentEndToEnd(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	userID, cookie, token := signUp(t, e)
	_ = userID

	order := placeOrderAndPayment(t, e, entry, cookie, token)

	// The order, its invoice and the invoice's lines are all really there, and the
	// invoice was opened before the payment rather than after it.
	if got := count(t, e, "SELECT count(*) FROM orders WHERE id = $1 AND status = 'pending'", order.orderID); got != 1 {
		t.Fatal("the order was not recorded as pending")
	}
	if got := count(t, e, "SELECT count(*) FROM invoices WHERE order_id = $1 AND status = 'open'", order.orderID); got != 1 {
		t.Fatal("the invoice was not opened with the order")
	}
	if got := count(t, e, `SELECT count(*) FROM invoice_items
		WHERE invoice_id IN (SELECT id FROM invoices WHERE order_id = $1)`, order.orderID); got != 1 {
		t.Fatal("the invoice is not itemised")
	}

	// A second payment attempt for the same order through the same gateway is refused,
	// because the idempotency key is derived from the order: the platform holds one
	// payment for it, not two.
	if rec := e.do(t, http.MethodPost, "/api/v1/orders/"+order.orderID+"/payments",
		`{"gateway":"`+testGatewayName+`"}`, cookie, token); rec.Code != http.StatusConflict {
		t.Errorf("a second payment attempt returned %d (%s)", rec.Code, rec.Body.String())
	}

	// ---- the callback ----------------------------------------------------------
	rec := e.do(t, http.MethodPost, "/api/v1/webhooks/payments/"+testGatewayName,
		string(order.callback), nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Settled        bool `json:"settled"`
			AlreadySettled bool `json:"already_settled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if !payload.Data.Settled {
		t.Error("the first delivery reported itself as a repeat")
	}

	e.assertSettledOnce(t, order.orderID, order.amountMinor)

	// ---- a forged callback is refused and changes nothing ----------------------
	// The amount is what an attacker would change: a callback for 9900 accepted as one
	// for 99 is the whole attack. Both forgeries are signed with the wrong key, so the
	// refusal is the signature's and not the settlement's.
	forgeries := []string{
		strings.Replace(string(order.callback), fmt.Sprintf(`"amount_minor":%d`, order.amountMinor), `"amount_minor":99`, 1),
		strings.Replace(string(order.callback), `"status":"succeeded"`, `"status":"failed"`, 1),
	}
	for i, forged := range forgeries {
		if forged == string(order.callback) {
			t.Fatalf("forgery %d changed nothing; it proves nothing", i)
		}
		if rec := e.do(t, http.MethodPost, "/api/v1/webhooks/payments/"+testGatewayName,
			forged, nil, ""); rec.Code == http.StatusOK {
			t.Errorf("forgery %d was accepted", i)
		}
	}
	if got := count(t, e, `SELECT count(*) FROM ledger_transactions WHERE reference_type = 'payment'
		AND reference_id IN (SELECT id FROM payments WHERE order_id = $1)`, order.orderID); got != 1 {
		t.Errorf("the forged callbacks moved the ledger: %d transactions", got)
	}
}

// TestGateTheSameCallbackDeliveredAHundredTimes is the Gate.
//
// The first round delivers one callback a hundred times in sequence; the second delivers
// another a hundred times at once. Each round uses its own order, so neither inherits the
// other's state, and each must end with exactly one settlement.
func TestGateTheSameCallbackDeliveredAHundredTimes(t *testing.T) {
	e := newE2E(t)
	entry := seedCatalog(t, e)
	_, cookie, token := signUp(t, e)

	deliver := func(order placedOrder) (settled int, failures []string) {
		for i := 0; i < 100; i++ {
			rec := e.do(t, http.MethodPost, "/api/v1/webhooks/payments/"+testGatewayName,
				string(order.callback), nil, "")
			if rec.Code != http.StatusOK {
				failures = append(failures, fmt.Sprintf("delivery %d answered %d", i+1, rec.Code))
				continue
			}
			var payload struct {
				Data struct {
					Settled bool `json:"settled"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				failures = append(failures, fmt.Sprintf("delivery %d: %v", i+1, err))
				continue
			}
			if payload.Data.Settled {
				settled++
			}
		}
		return settled, failures
	}

	// ---- sequential ------------------------------------------------------------
	first := placeOrderAndPayment(t, e, entry, cookie, token)
	settled, failures := deliver(first)
	if len(failures) != 0 {
		t.Fatalf("the sequential round failed: %v", failures)
	}
	if settled != 1 {
		t.Fatalf("%d of a hundred sequential deliveries performed the settlement", settled)
	}
	e.assertSettledOnce(t, first.orderID, first.amountMinor)

	// ---- concurrent ------------------------------------------------------------
	second := placeOrderAndPayment(t, e, entry, cookie, token)

	var wg sync.WaitGroup
	var mu sync.Mutex
	concurrentSettled := 0
	var concurrentFailures []string
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each caller has its own recorder: the router is safe for concurrent
			// requests, and sharing a recorder would measure the test rather than the
			// platform.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost,
				"/api/v1/webhooks/payments/"+testGatewayName,
				strings.NewReader(string(second.callback)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(e.gateway.SignatureHeader(), second.signature)
			req.RemoteAddr = "203.0.113.7:4321"
			e.router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code != http.StatusOK {
				concurrentFailures = append(concurrentFailures,
					fmt.Sprintf("status %d: %s", rec.Code, rec.Body.String()))
				return
			}
			var payload struct {
				Data struct {
					Settled bool `json:"settled"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				concurrentFailures = append(concurrentFailures, "the response is not the envelope")
				return
			}
			if payload.Data.Settled {
				concurrentSettled++
			}
		}()
	}
	wg.Wait()

	if len(concurrentFailures) != 0 {
		t.Fatalf("the concurrent round failed: %v", concurrentFailures[:])
	}
	if concurrentSettled != 1 {
		t.Fatalf("%d of a hundred concurrent settlements performed the settlement", concurrentSettled)
	}
	e.assertSettledOnce(t, second.orderID, second.amountMinor)
}

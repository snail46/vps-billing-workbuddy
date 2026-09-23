package commerce

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/ledger"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment"
)

// Errors reported by the service.
var (
	// ErrOrderNotFound reports an order that does not exist, or one that exists and is
	// not the caller's. They are the same error deliberately: an order identifier that
	// answered "exists, but not yours" would be a way to enumerate other customers'
	// orders.
	ErrOrderNotFound = errors.New("commerce: no such order")
	// ErrPaymentNotFound reports a payment the callback names that the platform has no
	// record of.
	ErrPaymentNotFound = errors.New("commerce: no such payment")
	// ErrAmountMismatch reports a callback whose amount disagrees with the payment.
	ErrAmountMismatch = errors.New("commerce: callback amount disagrees with the payment")
	// ErrCurrencyMismatch reports a callback in a currency the payment was not taken in.
	ErrCurrencyMismatch = errors.New("commerce: callback currency disagrees with the payment")
	// ErrOrderNotPayable reports a payment attempt against an order that is not pending.
	ErrOrderNotPayable = errors.New("commerce: order is not payable")
	// ErrUnknownGateway reports a callback for a gateway this platform does not take.
	ErrUnknownGateway = errors.New("commerce: unknown payment gateway")
	// ErrUnsupportedNotification reports a callback the platform cannot act on yet.
	ErrUnsupportedNotification = errors.New("commerce: callback status cannot be handled")
	// ErrPlanNotFound reports a slug with no plan behind it.
	ErrPlanNotFound = errors.New("commerce: no such plan")
	// ErrPaymentAlreadyStarted reports a second payment attempt for an order through
	// the same gateway.
	ErrPaymentAlreadyStarted = errors.New("commerce: a payment has already been started for this order")
	// ErrConflict reports a write that collided with something already recorded — a
	// number, most likely. It is distinct from the specific errors above so that a
	// caller can retry it without retrying, say, an order that is not payable.
	ErrConflict = errors.New("commerce: the record conflicts with one already stored")
)

// Event types, versioned as docs/09 requires. A consumer subscribes to a version, so a
// payload that changes shape is a new event type rather than a change to the old one.
const (
	EventPaymentSucceeded = "payment.succeeded.v1"
	EventPaymentFailed    = "payment.failed.v1"
)

// Aggregate types, used by the outbox row to say what the event is about.
const (
	AggregatePayment = "payment"
)

// Store is the persistence the commerce service needs.
//
// Every method is expressed in this package's types and holds no SQL, so the service's
// rules are testable without a database. The adapter is the only place that knows about
// sqlc, pgx or a constraint's SQLSTATE.
//
// WithinTransaction is what makes "Payment + Order + Ledger + Outbox in one
// transaction" (docs/04) expressible without the service importing a database driver:
// the adapter rebinds itself to a new transaction and hands that back, so the store and
// the ledger poster inside fn are all writing to the same one.
type Store interface {
	// WithinTransaction runs fn against a store bound to a new database transaction,
	// committing if fn returns nil and rolling back otherwise.
	WithinTransaction(ctx context.Context, fn func(Store) error) error

	// Poster returns the ledger poster for the connection this store is bound to. On a
	// transactional store it posts into that same transaction, which is what makes the
	// entries land with the payment and the order rather than beside them.
	Poster() *ledger.Poster

	// Catalogue.
	ListActiveProducts(ctx context.Context) ([]Product, error)
	ListActivePlans(ctx context.Context) ([]Plan, error)
	// PlanByID reads one plan. It reports ErrPlanNotFound when there is none, which the
	// service passes through as the error the caller sees.
	PlanByID(ctx context.Context, planID uuid.UUID) (Plan, error)

	// Orders.
	CreateOrder(ctx context.Context, order Order) error
	OrderForUser(ctx context.Context, orderID, userID uuid.UUID) (Order, error)
	ListOrdersForUser(ctx context.Context, userID uuid.UUID) ([]Order, error)
	// PayOrder moves an order from pending to paid and its invoice from open to paid.
	//
	// It reports ErrOrderNotFound when the order is not in a state that can be paid,
	// because a conditional update that returns no rows has to be reported rather than
	// assumed to have worked.
	PayOrder(ctx context.Context, orderID uuid.UUID, paidAt time.Time) error

	// Payments.
	CreatePayment(ctx context.Context, p Payment) error
	// FindPaymentByGatewayRef looks a payment up by the gateway's own identifier, which
	// is what a callback names.
	FindPaymentByGatewayRef(ctx context.Context, gateway, gatewayPaymentID string) (Payment, error)
	// SettlePayment moves a payment to succeeded, exactly once.
	//
	// It is a single conditional UPDATE, so under concurrent callbacks exactly one
	// caller sees a row returned. The boolean says whether this caller was the one; a
	// false result means the work was already done and **nothing further may be
	// written**, because the second settlement of the same money is what the Gate
	// exists to prevent.
	SettlePayment(ctx context.Context, paymentID uuid.UUID, paidAt time.Time) (Payment, bool, error)
	// FailPayment moves a payment to failed, exactly once, on the same terms.
	FailPayment(ctx context.Context, paymentID uuid.UUID, at time.Time) (Payment, bool, error)
	// AppendGatewayPayload records what a callback said, for the dispute that may
	// follow.
	AppendGatewayPayload(ctx context.Context, paymentID uuid.UUID, payload map[string]any) error

	// Invoices.
	CreateInvoice(ctx context.Context, invoice Invoice) error
	CreateInvoiceItems(ctx context.Context, invoiceID uuid.UUID, items []InvoiceItem) error

	// Outbox.
	RecordOutboxEvent(ctx context.Context, event OutboxEvent) error
}

// OutboxEvent is one event waiting to be published.
type OutboxEvent struct {
	ID            uuid.UUID
	EventType     string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       map[string]any
}

// Gateways is the set of gateways the platform takes, keyed by name.
type Gateways map[string]payment.Gateway

// Gateway returns the named one.
func (g Gateways) Gateway(name string) (payment.Gateway, error) {
	gateway, ok := g[name]
	if !ok || gateway == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownGateway, name)
	}
	return gateway, nil
}

// Service implements the commercial writes.
type Service struct {
	store   Store
	gateway Gateways
	now     func() time.Time
}

// Deps are the service's collaborators.
type Deps struct {
	Store Store
	// Gateways is keyed by name. A missing gateway is a configuration error that the
	// routes report rather than one the code guesses at.
	Gateways Gateways
	// Now is injectable so timestamps are testable.
	Now func() time.Time
}

// NewService builds the service.
func NewService(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("commerce: a store is required")
	}
	if len(deps.Gateways) == 0 {
		return nil, errors.New("commerce: at least one payment gateway is required")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: deps.Store, gateway: deps.Gateways, now: now}, nil
}

// ListActiveProducts returns the catalogue's products.
func (s *Service) ListActiveProducts(ctx context.Context) ([]Product, error) {
	return s.store.ListActiveProducts(ctx)
}

// ListActivePlans returns the plans that are on sale.
func (s *Service) ListActivePlans(ctx context.Context) ([]Plan, error) {
	return s.store.ListActivePlans(ctx)
}

// ListOrders returns a customer's orders, most recent first.
func (s *Service) ListOrders(ctx context.Context, userID uuid.UUID) ([]Order, error) {
	return s.store.ListOrdersForUser(ctx, userID)
}

// GetOrder returns one of a customer's orders.
//
// The owner is part of the lookup, so an order that belongs to somebody else is
// indistinguishable from one that does not exist.
func (s *Service) GetOrder(ctx context.Context, userID, orderID uuid.UUID) (Order, error) {
	return s.store.OrderForUser(ctx, orderID, userID)
}

// OrderLineRequest is what a client asks for: a plan and how many.
//
// It names the plan by identifier and carries no price, on purpose. The identifier is
// what the catalogue handed the client; the price is read from the catalogue here. See
// NewOrderLine.
type OrderLineRequest struct {
	PlanID   uuid.UUID
	Quantity int
}

// CreateOrder prices an order from the catalogue and records it.
//
// The unit price comes from the plan and not from the caller, the snapshots are frozen
// here, and the invoice is opened beside the order: docs/04 requires invoices to be
// their own record, and an invoice that appeared only after payment could not say what
// was asked for before it was paid.
func (s *Service) CreateOrder(ctx context.Context, userID uuid.UUID, requested []OrderLineRequest, discount money.Money) (Order, error) {
	lines := make([]OrderItem, 0, len(requested))
	for _, request := range requested {
		plan, err := s.store.PlanByID(ctx, request.PlanID)
		if err != nil {
			return Order{}, fmt.Errorf("%w: %s", ErrPlanNotFound, request.PlanID)
		}
		line, err := NewOrderLine(plan, request.Quantity)
		if err != nil {
			return Order{}, err
		}
		lines = append(lines, line)
	}

	order, err := NewOrder(userID, lines, discount, s.now())
	if err != nil {
		return Order{}, err
	}

	invoiceNo, err := NewInvoiceNumber(s.now())
	if err != nil {
		return Order{}, err
	}
	invoice := Invoice{
		ID:        uuid.New(),
		InvoiceNo: invoiceNo,
		UserID:    userID,
		OrderID:   order.ID,
		Status:    InvoiceOpen,
		Amount:    order.Total,
		// The lines say what was asked for, in the plan's own words rather than in a
		// summary: an invoice whose single line reads "1 order" cannot be checked
		// against the order it belongs to.
		Items: invoiceItemsFor(order.Items),
	}

	if err := s.store.WithinTransaction(ctx, func(tx Store) error {
		if err := tx.CreateOrder(ctx, order); err != nil {
			return fmt.Errorf("commerce: record the order: %w", err)
		}
		if err := tx.CreateInvoice(ctx, invoice); err != nil {
			return fmt.Errorf("commerce: open the invoice: %w", err)
		}
		if err := tx.CreateInvoiceItems(ctx, invoice.ID, invoice.Items); err != nil {
			return fmt.Errorf("commerce: itemise the invoice: %w", err)
		}
		return nil
	}); err != nil {
		return Order{}, err
	}
	return order, nil
}

// invoiceItemsFor renders an order's lines as invoice lines.
//
// The amounts are recomputed rather than copied, because the two records disagreeing is
// exactly what must never happen: the invoice is the document, the order is the
// agreement, and the schema constrains each total to unit price times quantity.
func invoiceItemsFor(lines []OrderItem) []InvoiceItem {
	items := make([]InvoiceItem, 0, len(lines))
	for i := range lines {
		line := lines[i]
		total, err := line.Total()
		if err != nil {
			// Unreachable for a line built by NewOrderLine, which has already priced
			// and bounded the quantity. Returning what has been built so far would be
			// an invoice missing lines; failing loudly is the honest answer.
			panic(fmt.Sprintf("commerce: re-price an order line: %v", err))
		}
		items = append(items, InvoiceItem{
			ID:          uuid.New(),
			Description: line.PlanSnapshot.Name,
			Quantity:    line.Quantity,
			UnitAmount:  line.UnitPrice,
			Total:       total,
		})
	}
	return items
}

// StartPayment asks a gateway to take an order's money and records the attempt.
//
// The idempotency key is derived from the order, the gateway and the day rather than
// being random, so a client that retries cannot create a second payment for the same
// order. That is what makes the column's UNIQUE constraint usable rather than
// decorative: a key has to be a function of the thing being paid.
func (s *Service) StartPayment(ctx context.Context, userID, orderID uuid.UUID, gatewayName string) (Payment, payment.Intent, error) {
	gateway, err := s.gateway.Gateway(gatewayName)
	if err != nil {
		return Payment{}, payment.Intent{}, err
	}

	order, err := s.store.OrderForUser(ctx, orderID, userID)
	if err != nil {
		return Payment{}, payment.Intent{}, err
	}
	if order.Status != OrderPending {
		return Payment{}, payment.Intent{}, fmt.Errorf("%w: it is %s", ErrOrderNotPayable, order.Status)
	}

	now := s.now()
	paymentNo, err := NewPaymentNumber(now)
	if err != nil {
		return Payment{}, payment.Intent{}, err
	}

	intent, err := gateway.CreateIntent(ctx, payment.CreateIntentRequest{
		PaymentNo:   paymentNo,
		Amount:      order.Total,
		Description: "order " + order.OrderNo,
	})
	if err != nil {
		return Payment{}, payment.Intent{}, err
	}

	key := paymentIdempotencyKey(order.ID, gatewayName, now)

	p := Payment{
		ID:               uuid.New(),
		PaymentNo:        paymentNo,
		OrderID:          order.ID,
		Gateway:          gatewayName,
		GatewayPaymentID: intent.GatewayPaymentID,
		Status:           PaymentPending,
		Amount:           order.Total,
		IdempotencyKey:   key,
	}

	if err := s.store.CreatePayment(ctx, p); err != nil {
		if errors.Is(err, ErrPaymentAlreadyStarted) {
			return Payment{}, payment.Intent{}, ErrPaymentAlreadyStarted
		}
		return Payment{}, payment.Intent{}, err
	}
	return p, intent, nil
}

// paymentIdempotencyKey derives the key from what is being paid.
//
// Hashed rather than concatenated so the stored key does not read as an order id and a
// gateway name in a way that invites another caller to construct one.
// The hash functions it builds on cannot fail for these inputs, so there is no error to
// propagate.
func paymentIdempotencyKey(orderID uuid.UUID, gateway string, now time.Time) string {
	mac := hmac.New(sha256.New, []byte("commerce:payment-start:"+now.UTC().Format("2006-01-02")))
	_, _ = mac.Write([]byte(orderID.String()))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(gateway))
	return "ord:" + hex.EncodeToString(mac.Sum(nil))[:32]
}

// SettleResult is what a callback produced.
type SettleResult struct {
	Payment Payment
	Order   Order
	// AlreadySettled is true when this notification repeated one already applied, and
	// nothing was written. It is reported rather than hidden behind a success, because
	// an operator reading the logs needs to tell "settled now" from "settled earlier by
	// someone else".
	AlreadySettled bool
}

// Settle applies a gateway callback.
//
// The signature is verified before anything is looked up, so a forged callback costs
// nothing but the verification. Everything else — the payment's transition, the
// order's, the ledger posting and the outbox row — happens inside one database
// transaction, which is what docs/04 requires and the reason the outbox row cannot be
// written by a later step.
func (s *Service) Settle(ctx context.Context, gatewayName string, body []byte, signature string) (SettleResult, error) {
	gateway, err := s.gateway.Gateway(gatewayName)
	if err != nil {
		return SettleResult{}, err
	}

	notification, err := gateway.Verify(body, signature)
	if err != nil {
		return SettleResult{}, err
	}

	switch notification.Status {
	case "succeeded":
		return s.settle(ctx, gatewayName, notification)
	case "failed":
		return s.fail(ctx, gatewayName, notification)
	default:
		// Loud rather than silent. A status the platform does not handle is a gap in
		// its own design, and answering 200 would tell the gateway to stop trying while
		// nothing had been recorded.
		return SettleResult{}, fmt.Errorf("%w: %q", ErrUnsupportedNotification, notification.Status)
	}
}

// settle records that money arrived.
//
// The conditional transition is the gate: everything after it runs only for the caller
// that won it, which is what makes a hundred deliveries of the same callback —
// sequential or concurrent — write exactly one settlement.
func (s *Service) settle(ctx context.Context, gatewayName string, notification payment.Notification) (SettleResult, error) {
	// Looked up outside the transaction: this read only fetches the record the callback
	// names, and what decides whether this caller settles is the conditional update
	// inside the transaction. Doing it inside would hold the transaction open for no
	// additional guarantee.
	stored, err := s.store.FindPaymentByGatewayRef(ctx, gatewayName, notification.GatewayPaymentID)
	if err != nil {
		return SettleResult{}, err
	}

	// A callback for a different amount is not a replay of this payment: someone is
	// asserting the customer paid a different sum than the one recorded, and accepting
	// it silently would leave the platform's record and the gateway's disagreeing while
	// both sides believed they agreed.
	if stored.Amount.AmountMinor != notification.Amount.AmountMinor {
		return SettleResult{}, fmt.Errorf("%w: payment holds %d, the callback says %d",
			ErrAmountMismatch, stored.Amount.AmountMinor, notification.Amount.AmountMinor)
	}
	if stored.Amount.Currency != notification.Amount.Currency {
		return SettleResult{}, fmt.Errorf("%w: payment holds %s, the callback says %s",
			ErrCurrencyMismatch, stored.Amount.Currency, notification.Amount.Currency)
	}

	if PaymentIsSettled(stored.Status) {
		// Nothing is written, and the caller is told the notification was met. That is
		// the correct answer to a redelivered callback: the gateway's obligation was to
		// notify, and it has.
		return SettleResult{Payment: stored, AlreadySettled: true}, nil
	}

	now := s.now()
	posting := ledger.Transaction{
		ID:            uuid.New(),
		Type:          ledger.TypePaymentSettlement,
		ReferenceType: "payment",
		ReferenceID:   stored.ID,
		Description:   "payment " + stored.PaymentNo,
		// Money arriving from a gateway: the clearing asset increases by a debit, and
		// the platform's income increases by the matching credit. No wallet entries —
		// the customer has not been given balance, they have paid for an order.
		Entries: []ledger.Entry{
			{
				AccountType: ledger.AccountGatewayClearing,
				AccountID:   ledger.AccountIDGatewayClearing,
				Direction:   ledger.DirectionDebit,
				Amount:      stored.Amount,
			},
			{
				AccountType: ledger.AccountRevenue,
				AccountID:   ledger.AccountIDRevenue,
				Direction:   ledger.DirectionCredit,
				Amount:      stored.Amount,
			},
		},
	}

	var result SettleResult
	err = s.store.WithinTransaction(ctx, func(tx Store) error {
		// The transition is the gate: the caller that sees a row returned is the one
		// that settles, and the others see false and write nothing.
		paid, applied, err := tx.SettlePayment(ctx, stored.ID, now)
		if err != nil {
			return err
		}
		if !applied {
			result = SettleResult{Payment: paid, AlreadySettled: true}
			return nil
		}

		if err := tx.PayOrder(ctx, paid.OrderID, now); err != nil {
			return err
		}

		// The wallet projection is wired because a poster that could produce wallet
		// entries without maintaining them would be a latent bug. A settlement
		// produces none, so the projection has nothing to do here.
		if err := tx.Poster().Post(ctx, posting); err != nil {
			return err
		}

		if err := tx.RecordOutboxEvent(ctx, OutboxEvent{
			ID:            uuid.New(),
			EventType:     EventPaymentSucceeded,
			AggregateType: AggregatePayment,
			AggregateID:   paid.ID,
			Payload: map[string]any{
				"payment_no":         paid.PaymentNo,
				"order_id":           paid.OrderID.String(),
				"gateway":            gatewayName,
				"gateway_payment_id": paid.GatewayPaymentID,
				"amount_minor":       paid.Amount.AmountMinor,
				"currency":           string(paid.Amount.Currency),
				"paid_at":            now.UTC().Format(time.RFC3339),
			},
		}); err != nil {
			return err
		}

		result = SettleResult{Payment: paid}
		return nil
	})
	if err != nil {
		return SettleResult{}, err
	}
	return result, nil
}

// fail records that a payment did not go through.
//
// It is handled because a gateway will send it, and a callback the platform refuses
// would be retried forever on both sides. No ledger entry is written: no money moved.
// The order stays pending, so the customer can try again.
func (s *Service) fail(ctx context.Context, gatewayName string, notification payment.Notification) (SettleResult, error) {
	stored, err := s.store.FindPaymentByGatewayRef(ctx, gatewayName, notification.GatewayPaymentID)
	if err != nil {
		return SettleResult{}, err
	}
	if PaymentIsSettled(stored.Status) {
		// A failure notification for a payment that already succeeded is the two
		// sources disagreeing, and the same rule applies as for an amount: the
		// disagreement is reported rather than absorbed.
		return SettleResult{}, fmt.Errorf("%w: the payment already settled, the gateway now reports %q",
			ErrUnsupportedNotification, notification.Status)
	}

	now := s.now()
	var result SettleResult
	err = s.store.WithinTransaction(ctx, func(tx Store) error {
		failed, applied, err := tx.FailPayment(ctx, stored.ID, now)
		if err != nil {
			return err
		}
		if !applied {
			result = SettleResult{Payment: failed, AlreadySettled: true}
			return nil
		}

		if err := tx.AppendGatewayPayload(ctx, stored.ID, notification.Payload); err != nil {
			return err
		}
		if err := tx.RecordOutboxEvent(ctx, OutboxEvent{
			ID:            uuid.New(),
			EventType:     EventPaymentFailed,
			AggregateType: AggregatePayment,
			AggregateID:   failed.ID,
			Payload: map[string]any{
				"payment_no":         failed.PaymentNo,
				"order_id":           failed.OrderID.String(),
				"gateway":            gatewayName,
				"gateway_payment_id": failed.GatewayPaymentID,
				"amount_minor":       failed.Amount.AmountMinor,
				"currency":           string(failed.Amount.Currency),
			},
		}); err != nil {
			return err
		}

		result = SettleResult{Payment: failed}
		return nil
	})
	if err != nil {
		return SettleResult{}, err
	}
	return result, nil
}

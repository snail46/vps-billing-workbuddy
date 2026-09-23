package commerce

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// The commercial records this phase works with.
//
// They are the parts of the tables the domain reasons about, not the tables: the
// adapter maps rows onto these, and nothing here knows about pgtype, sqlc or a
// database. That is what lets a rule be tested without one — and, more usefully, what
// keeps the rule from being restated in the adapter.

// Product statuses.
const (
	ProductDraft    = "draft"
	ProductActive   = "active"
	ProductArchived = "archived"
)

// Plan statuses.
const (
	PlanDraft    = "draft"
	PlanActive   = "active"
	PlanArchived = "archived"
)

// Invoice statuses. `docs/05` defines no invoice machine, so these are this
// repository's vocabulary (ADR-005); the column's CHECK constraint means extending it
// is a migration rather than a typo.
const (
	InvoiceOpen = "open"
	InvoicePaid = "paid"
	InvoiceVoid = "void"
)

// Errors.
var (
	// ErrPlanNotPurchaseable reports a plan that is not on sale.
	ErrPlanNotPurchaseable = errors.New("commerce: plan is not available")
	// ErrInvalidQuantity reports a quantity outside 1..MaxOrderQuantity.
	ErrInvalidQuantity = errors.New("commerce: quantity is out of range")
	// ErrEmptyOrder reports an order with no lines.
	ErrEmptyOrder = errors.New("commerce: an order needs at least one line")
	// ErrMixedCurrencies reports an order whose lines are not all in one currency.
	ErrMixedCurrencies = errors.New("commerce: an order must be in a single currency")
	// ErrInvalidAmount reports a price or a total below zero.
	ErrInvalidAmount = errors.New("commerce: amount must not be negative")
	// ErrInvalidDecimal reports a decimal literal that is not well formed.
	ErrInvalidDecimal = errors.New("commerce: decimal is not well formed")
)

// MaxOrderQuantity bounds a single line.
//
// Not a business rule anybody asked for, but an unbounded quantity multiplied into a
// total is an overflow waiting for someone to type a large number: the column is a
// bigint and the CHECK constraint asserts the product, so a quantity that overflows
// would violate the constraint rather than the total silently wrapping.
const MaxOrderQuantity = 1000

// decimalShape is the shape of a decimal literal: digits, optionally with one or two
// fractional digits.
//
// `plans.cpu_cores` is `numeric(10,2)`. It is carried as a string rather than as a
// float because a float cannot represent 0.1 exactly, and a vCPU count that reads back
// as 1.9999999 is the first symptom of a rounding decision that was never made.
var decimalShape = regexp.MustCompile(`^\d+(\.\d{1,2})?$`)

// Product is a catalogue entry.
type Product struct {
	ID     uuid.UUID
	Slug   string
	Name   map[string]string
	Status string
}

// Plan is a purchaseable configuration of a product.
type Plan struct {
	ID             uuid.UUID
	ProductID      uuid.UUID
	Slug           string
	Name           map[string]string
	Status         string
	CpuCores       string
	MemoryMB       int
	DiskGB         int
	Virtualization string
	BillingCycle   string
	Price          money.Money
}

// Purchaseable reports whether a plan may be added to an order.
//
// Only `active`. A draft is still being written and an archived one is kept for the
// orders that reference it, so neither is on sale — and stating that here means a
// handler cannot decide differently.
func (p Plan) Purchaseable() bool { return p.Status == PlanActive }

// OrderItem is one line of an order.
//
// UnitPrice and the snapshots are records of a moment, not references: nothing in the
// codebase updates them after the row is written (ADR-005), which is what makes an
// order immune to a later price change.
type OrderItem struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	PlanID    uuid.UUID
	Quantity  int
	UnitPrice money.Money
	// PlanSnapshot is what the plan looked like when the order was placed.
	PlanSnapshot PlanSnapshot
}

// Total returns the line total.
func (i OrderItem) Total() (money.Money, error) {
	return i.UnitPrice.Times(int64(i.Quantity))
}

// Order is a customer's commitment to buy.
type Order struct {
	ID       uuid.UUID
	OrderNo  string
	UserID   uuid.UUID
	Status   string
	Subtotal money.Money
	Discount money.Money
	Total    money.Money
	Items    []OrderItem
	PaidAt   *time.Time
	// CreatedAt is when the order was placed. A list of orders without a date is a
	// list nobody can reason about, and omitting it now would be an API change later.
	CreatedAt time.Time
}

// Payment records an attempt to pay an order.
//
// It is a separate record from the order because the two answer different questions:
// the order is what the customer agreed to buy, and the payment is what a gateway was
// asked for and did. `docs/04` requires them separate, and it is also what lets one
// order be paid in two attempts without the order being rewritten.
type Payment struct {
	ID        uuid.UUID
	PaymentNo string
	OrderID   uuid.UUID
	Gateway   string
	// GatewayPaymentID is the gateway's identifier. It is nil until the gateway has
	// been asked, which is why the unique index over it is partial.
	GatewayPaymentID string
	Status           string
	Amount           money.Money
	IdempotencyKey   string
	PaidAt           *time.Time
}

// Invoice is the document asking for an order's money.
type Invoice struct {
	ID        uuid.UUID
	InvoiceNo string
	UserID    uuid.UUID
	OrderID   uuid.UUID
	Status    string
	Amount    money.Money
	// Items are the lines the amount is made of. They are built from the order's lines
	// and are never edited afterwards, for the same reason the order's snapshots are
	// not: the document and the agreement have to agree.
	Items []InvoiceItem
}

// InvoiceItem is one line of an invoice.
type InvoiceItem struct {
	ID          uuid.UUID
	Description map[string]string
	Quantity    int
	UnitAmount  money.Money
	Total       money.Money
}

// PlanSnapshot is a plan as it was at the moment of purchase.
//
// A separate type from Plan on purpose. It is written to a JSON column and read back
// by an interface, so it must not change shape when the catalogue does: if it were the
// same type, adding a field to Plan would silently change what every historical
// snapshot is decoded into. The JSON tags are the contract with those rows.
type PlanSnapshot struct {
	PlanID         string            `json:"plan_id"`
	Slug           string            `json:"slug"`
	Name           map[string]string `json:"name"`
	CpuCores       string            `json:"cpu_cores"`
	MemoryMB       int               `json:"memory_mb"`
	DiskGB         int               `json:"disk_gb"`
	Virtualization string            `json:"virtualization"`
	BillingCycle   string            `json:"billing_cycle"`
	PriceMinor     int64             `json:"price_minor"`
	Currency       string            `json:"currency"`
}

// NewPlanSnapshot freezes a plan.
func NewPlanSnapshot(plan Plan) PlanSnapshot {
	// The map is copied rather than referenced: the catalogue's map may be shared with
	// whatever read it, and a snapshot that changes when its source does is not a
	// snapshot.
	names := make(map[string]string, len(plan.Name))
	for locale, name := range plan.Name {
		names[locale] = name
	}

	return PlanSnapshot{
		PlanID:         plan.ID.String(),
		Slug:           plan.Slug,
		Name:           names,
		CpuCores:       plan.CpuCores,
		MemoryMB:       plan.MemoryMB,
		DiskGB:         plan.DiskGB,
		Virtualization: plan.Virtualization,
		BillingCycle:   plan.BillingCycle,
		PriceMinor:     plan.Price.AmountMinor,
		Currency:       string(plan.Price.Currency),
	}
}

// NewOrderLine builds a line for a plan at a quantity.
//
// The price comes from the plan and not from the caller: a client that could send its
// own unit price could buy at any price it liked, and the validation for that is not
// "check it matches" but "do not accept one".
func NewOrderLine(plan Plan, quantity int) (OrderItem, error) {
	if !plan.Purchaseable() {
		return OrderItem{}, fmt.Errorf("%w: %s is %s", ErrPlanNotPurchaseable, plan.Slug, plan.Status)
	}
	if quantity < 1 || quantity > MaxOrderQuantity {
		return OrderItem{}, fmt.Errorf("%w: %d is outside 1..%d", ErrInvalidQuantity, quantity, MaxOrderQuantity)
	}
	if plan.Price.IsNegative() {
		return OrderItem{}, fmt.Errorf("%w: plan %s costs %d", ErrInvalidAmount, plan.Slug, plan.Price.AmountMinor)
	}
	if err := ValidateDecimal(plan.CpuCores); err != nil {
		return OrderItem{}, fmt.Errorf("plan %s: %w", plan.Slug, err)
	}

	return OrderItem{
		ID:           uuid.New(),
		ProductID:    plan.ProductID,
		PlanID:       plan.ID,
		Quantity:     quantity,
		UnitPrice:    plan.Price,
		PlanSnapshot: NewPlanSnapshot(plan),
	}, nil
}

// NewOrder assembles an order from its lines.
//
// The totals are computed here and nowhere else. A handler that computed its own
// total would be a second implementation of the same arithmetic, and the schema's
// CHECK constraint would catch the disagreement only for the order it belongs to.
func NewOrder(userID uuid.UUID, lines []OrderItem, discount money.Money, now time.Time) (Order, error) {
	if len(lines) == 0 {
		return Order{}, ErrEmptyOrder
	}

	// The currency is taken from the first line, and the accumulator starts in it: a
	// zero Money with an empty currency cannot be added to, and the failure would only
	// be discovered on the first addition rather than stated here. The test that caught
	// this is TestNewOrderTotalsItsLines.
	currency := lines[0].UnitPrice.Currency
	if currency == "" {
		return Order{}, errors.New("commerce: order line has no currency")
	}
	subtotal := money.Zero(currency)

	for i := range lines {
		line := lines[i]
		if line.UnitPrice.Currency != currency {
			// An order in two currencies would have a single total column and two
			// meanings. The platform sells in one currency per order and settles a
			// conversion as its own transaction, which is the same reason the ledger
			// balances per currency.
			return Order{}, fmt.Errorf("%w: %s and %s", ErrMixedCurrencies, currency, line.UnitPrice.Currency)
		}
		total, err := line.Total()
		if err != nil {
			return Order{}, err
		}
		if subtotal, err = subtotal.Add(total); err != nil {
			return Order{}, err
		}
	}

	if discount.Currency != currency {
		return Order{}, fmt.Errorf("%w: discount is in %s, order is in %s", ErrMixedCurrencies, discount.Currency, currency)
	}
	if discount.IsNegative() {
		return Order{}, fmt.Errorf("%w: discount is %d", ErrInvalidAmount, discount.AmountMinor)
	}

	total, err := subtotal.Sub(discount)
	if err != nil {
		return Order{}, err
	}
	if total.IsNegative() {
		return Order{}, fmt.Errorf("%w: a discount of %d exceeds a subtotal of %d",
			ErrInvalidAmount, discount.AmountMinor, subtotal.AmountMinor)
	}

	number, err := NewOrderNumber(now)
	if err != nil {
		return Order{}, err
	}

	return Order{
		ID:        uuid.New(),
		OrderNo:   number,
		UserID:    userID,
		Status:    OrderPending,
		Subtotal:  subtotal,
		Discount:  discount,
		Total:     total,
		Items:     lines,
		CreatedAt: now.UTC(),
	}, nil
}

// ValidateDecimal checks a decimal literal.
func ValidateDecimal(value string) error {
	if !decimalShape.MatchString(value) {
		return fmt.Errorf("%w: %q", ErrInvalidDecimal, value)
	}
	return nil
}

// randomSuffix returns a short random token for a human-readable number.
//
// Crockford's base32 without the ambiguous letters, because these numbers are read
// aloud and typed by hand: a support conversation that has to distinguish O from 0
// costs more than the two characters saved.
var numberEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func randomSuffix(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("commerce: generate a number: %w", err)
	}
	return numberEncoding.EncodeToString(buf), nil
}

// NewOrderNumber returns a customer-facing order number.
//
// Prefixed by kind and dated, so an operator reading one out of an email can tell what
// it refers to and roughly when. The random part makes it unguessable: a sequential
// number would let anyone enumerate the platform's order volume, and would tell a
// customer how many orders exist besides theirs.
func NewOrderNumber(now time.Time) (string, error) { return newNumber("ORD", now) }

// NewPaymentNumber returns a payment number.
func NewPaymentNumber(now time.Time) (string, error) { return newNumber("PAY", now) }

// NewInvoiceNumber returns an invoice number.
func NewInvoiceNumber(now time.Time) (string, error) { return newNumber("INV", now) }

func newNumber(prefix string, now time.Time) (string, error) {
	suffix, err := randomSuffix(10)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", prefix, now.UTC().Format("20060102"), suffix), nil
}

// NormalizeStatus trims and lower-cases a status read from a client or a request.
//
// A gateway sends what it sends; the platform's vocabulary is lower case, and a
// comparison that failed because of a capital letter would be a settlement that
// silently did not happen.
func NormalizeStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

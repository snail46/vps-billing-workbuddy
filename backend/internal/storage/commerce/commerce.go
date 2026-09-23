// Package commercestore adapts the generated queries to the interfaces the commerce
// package declares.
//
// It is a package of its own rather than part of internal/db for the reason that
// package states explicitly: it owns connection management and never knows about a
// table. This one exists to know about the commercial ones, and it sits beside the
// identity adapter, which made the same choice.
//
// Everything database-specific stops here: the SQLSTATE codes, the handling of NULL,
// the driver's row types. The service's rules stay expressed in their own vocabulary,
// and the parts that can be wrong without a database are testable.
package commercestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/ledger"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// uniqueViolation is PostgreSQL's SQLSTATE for a unique constraint violation.
const uniqueViolation = "23505"

// Store implements commerce.Store.
type Store struct {
	queries *sqlcgen.Queries
	db      sqlcgen.DBTX
	// pool is nil when the store is bound to a transaction, which is how
	// WithinTransaction knows whether it has to begin one or only a savepoint.
	pool *pgxpool.Pool
	tx   pgx.Tx
}

// New returns a store over a pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool), db: pool, pool: pool}
}

// newBound returns a store whose statements run inside the given transaction.
func newBound(pool *pgxpool.Pool, tx pgx.Tx) *Store {
	return &Store{queries: sqlcgen.New(tx), db: tx, pool: pool, tx: tx}
}

var (
	_ commerce.Store    = (*Store)(nil)
	_ ledger.Store      = (*ledgerStore)(nil)
	_ ledger.Projection = (*walletProjection)(nil)
)

// WithinTransaction runs fn against a store bound to a new transaction.
//
// Nested calls use a savepoint rather than a second transaction, which is what makes a
// service method that opens its own transaction callable from another one that has
// already opened theirs: the inner failure rolls back to the outer state instead of
// ending the outer work.
func (s *Store) WithinTransaction(ctx context.Context, fn func(commerce.Store) error) error {
	if s.tx == nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("commercestore: begin: %w", err)
		}
		if err := fn(newBound(s.pool, tx)); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		return tx.Commit(ctx)
	}

	nested, err := s.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("commercestore: begin nested: %w", err)
	}
	if err := fn(newBound(s.pool, nested)); err != nil {
		_ = nested.Rollback(ctx)
		return err
	}
	return nested.Commit(ctx)
}

// Poster returns the ledger poster for this store's connection.
//
// The ledger adapter and the wallet projection are built from the same `DBTX`, which is
// the property the settlement depends on: the entries and the projection they summarise
// are written by the same statements, so a rollback takes both back.
func (s *Store) Poster() *ledger.Poster {
	return ledger.NewPoster(&ledgerStore{queries: s.queries}, &walletProjection{queries: s.queries})
}

// --------------------------------------------------------------- catalogue --

func (s *Store) ListActiveProducts(ctx context.Context) ([]commerce.Product, error) {
	rows, err := s.queries.ListActiveProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("commercestore: list products: %w", err)
	}
	products := make([]commerce.Product, 0, len(rows))
	for i := range rows {
		product, err := toProduct(rows[i])
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}
	return products, nil
}

func (s *Store) ListActivePlans(ctx context.Context) ([]commerce.Plan, error) {
	rows, err := s.queries.ListActivePlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("commercestore: list plans: %w", err)
	}
	plans := make([]commerce.Plan, 0, len(rows))
	for i := range rows {
		plan, err := toPlan(planRow(rows[i]))
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (s *Store) PlanByID(ctx context.Context, planID uuid.UUID) (commerce.Plan, error) {
	row, err := s.queries.PlanByID(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Plan{}, commerce.ErrPlanNotFound
		}
		return commerce.Plan{}, fmt.Errorf("commercestore: read plan: %w", err)
	}
	return toPlan(planRow(row))
}

// ------------------------------------------------------------------- orders --

func (s *Store) CreateOrder(ctx context.Context, order commerce.Order) error {
	paidAt := nullableTime(order.PaidAt)
	if err := s.queries.CreateOrder(ctx, sqlcgen.CreateOrderParams{
		ID:            order.ID,
		OrderNo:       order.OrderNo,
		UserID:        order.UserID,
		Status:        order.Status,
		SubtotalMinor: order.Subtotal.AmountMinor,
		DiscountMinor: order.Discount.AmountMinor,
		TotalMinor:    order.Total.AmountMinor,
		Currency:      string(order.Total.Currency),
		PaidAt:        paidAt,
	}); err != nil {
		return mapWriteError(err)
	}

	for i := range order.Items {
		item := order.Items[i]
		// The product's snapshot is its identifier plus nothing else: the product's
		// own fields are not part of what a customer bought, and the plan's snapshot
		// is where the agreement lives.
		productSnapshot, err := json.Marshal(map[string]string{"product_id": item.ProductID.String()})
		if err != nil {
			return fmt.Errorf("commercestore: encode the product snapshot: %w", err)
		}
		planSnapshot, err := json.Marshal(item.PlanSnapshot)
		if err != nil {
			return fmt.Errorf("commercestore: encode the plan snapshot: %w", err)
		}

		quantity, err := quantityColumn(item.Quantity)
		if err != nil {
			return err
		}
		if err := s.queries.CreateOrderItem(ctx, sqlcgen.CreateOrderItemParams{
			ID:              item.ID,
			OrderID:         order.ID,
			ProductID:       item.ProductID,
			PlanID:          item.PlanID,
			Quantity:        quantity,
			UnitPriceMinor:  item.UnitPrice.AmountMinor,
			TotalMinor:      mustLineTotal(item),
			ProductSnapshot: productSnapshot,
			PlanSnapshot:    planSnapshot,
		}); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

// quantityColumn narrows a line's quantity to the column's type.
//
// The domain bounds it to 1..MaxOrderQuantity before it reaches here, so the conversion
// is provably safe rather than true of the call path; asserting the bound here is what
// makes that visible at the conversion instead of three packages away.
func quantityColumn(quantity int) (int32, error) {
	if quantity < 1 || quantity > commerce.MaxOrderQuantity {
		return 0, fmt.Errorf("commercestore: quantity %d is outside 1..%d", quantity, commerce.MaxOrderQuantity)
	}
	return int32(quantity), nil
}

// mustLineTotal re-prices a line for storage. The service has already priced and
// bounded the quantity, so the only way this fails is a line built outside the domain —
// and storing that would be storing a number nobody can derive.
func mustLineTotal(item commerce.OrderItem) int64 {
	total, err := item.Total()
	if err != nil {
		panic(fmt.Sprintf("commercestore: re-price an order line: %v", err))
	}
	return total.AmountMinor
}

func (s *Store) OrderForUser(ctx context.Context, orderID, userID uuid.UUID) (commerce.Order, error) {
	row, err := s.queries.OrderByIDForUser(ctx, sqlcgen.OrderByIDForUserParams{ID: orderID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The same answer whether the order does not exist or belongs to someone
			// else: distinguishing them is how one customer's order identifiers could
			// be used to enumerate another's.
			return commerce.Order{}, commerce.ErrOrderNotFound
		}
		return commerce.Order{}, fmt.Errorf("commercestore: read order: %w", err)
	}

	items, err := s.orderItems(ctx, orderID)
	if err != nil {
		return commerce.Order{}, err
	}
	return toOrder(row, items)
}

func (s *Store) ListOrdersForUser(ctx context.Context, userID uuid.UUID) ([]commerce.Order, error) {
	rows, err := s.queries.ListOrdersForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("commercestore: list orders: %w", err)
	}
	orders := make([]commerce.Order, 0, len(rows))
	for i := range rows {
		items, err := s.orderItems(ctx, rows[i].ID)
		if err != nil {
			return nil, err
		}
		order, err := toOrder(rows[i], items)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *Store) orderItems(ctx context.Context, orderID uuid.UUID) ([]commerce.OrderItem, error) {
	rows, err := s.queries.OrderItemsForOrder(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("commercestore: read the order's lines: %w", err)
	}
	items := make([]commerce.OrderItem, 0, len(rows))
	for i := range rows {
		item, err := toOrderItem(rows[i])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// PayOrder moves the order and its invoice in the one statement each.
//
// Both are conditional updates, and a zero-row result is reported rather than assumed
// to have worked: the order might already be paid — a redelivered callback that lost a
// race — or cancelled, and the settlement has to know which.
func (s *Store) PayOrder(ctx context.Context, orderID uuid.UUID, paidAt time.Time) error {
	stamp := pgtype.Timestamptz{Time: paidAt, Valid: true}

	tag, err := s.queries.PayOrder(ctx, sqlcgen.PayOrderParams{ID: orderID, PaidAt: stamp})
	if err != nil {
		return fmt.Errorf("commercestore: pay the order: %w", err)
	}
	if tag == 0 {
		return commerce.ErrOrderNotFound
	}

	tag, err = s.queries.MarkInvoicePaid(ctx, sqlcgen.MarkInvoicePaidParams{OrderID: &orderID, PaidAt: stamp})
	if err != nil {
		return fmt.Errorf("commercestore: pay the invoice: %w", err)
	}
	if tag == 0 {
		// The order moved and the invoice did not. Inside a transaction this rolls back
		// with the rest; reached at all, it means the two records have diverged, which
		// is a state to report rather than to paper over.
		return errors.New("commercestore: the order has no open invoice to pay")
	}
	return nil
}

// ----------------------------------------------------------------- invoices --

func (s *Store) CreateInvoice(ctx context.Context, invoice commerce.Invoice) error {
	if err := s.queries.CreateInvoice(ctx, sqlcgen.CreateInvoiceParams{
		ID:          invoice.ID,
		InvoiceNo:   invoice.InvoiceNo,
		UserID:      invoice.UserID,
		OrderID:     invoice.OrderID,
		Status:      invoice.Status,
		AmountMinor: invoice.Amount.AmountMinor,
		Currency:    string(invoice.Amount.Currency),
	}); err != nil {
		return mapWriteError(err)
	}
	return nil
}

func (s *Store) CreateInvoiceItems(ctx context.Context, invoiceID uuid.UUID, items []commerce.InvoiceItem) error {
	for i := range items {
		item := items[i]
		description, err := json.Marshal(item.Description)
		if err != nil {
			return fmt.Errorf("commercestore: encode the description: %w", err)
		}
		quantity, err := quantityColumn(item.Quantity)
		if err != nil {
			return err
		}
		if err := s.queries.CreateInvoiceItem(ctx, sqlcgen.CreateInvoiceItemParams{
			ID:              item.ID,
			InvoiceID:       invoiceID,
			DescriptionI18n: description,
			Quantity:        quantity,
			UnitAmountMinor: item.UnitAmount.AmountMinor,
			TotalMinor:      item.Total.AmountMinor,
		}); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

// ----------------------------------------------------------------- payments --

func (s *Store) CreatePayment(ctx context.Context, p commerce.Payment) error {
	if err := s.queries.CreatePayment(ctx, sqlcgen.CreatePaymentParams{
		ID:               p.ID,
		PaymentNo:        p.PaymentNo,
		OrderID:          p.OrderID,
		Gateway:          p.Gateway,
		GatewayPaymentID: nullableText(p.GatewayPaymentID),
		Status:           p.Status,
		AmountMinor:      p.Amount.AmountMinor,
		Currency:         string(p.Amount.Currency),
		IdempotencyKey:   p.IdempotencyKey,
	}); err != nil {
		return mapWriteError(err)
	}
	return nil
}

func (s *Store) FindPaymentByGatewayRef(ctx context.Context, gateway, gatewayPaymentID string) (commerce.Payment, error) {
	row, err := s.queries.PaymentByGatewayRef(ctx, sqlcgen.PaymentByGatewayRefParams{
		Gateway:          gateway,
		GatewayPaymentID: nullableText(gatewayPaymentID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Payment{}, commerce.ErrPaymentNotFound
		}
		return commerce.Payment{}, fmt.Errorf("commercestore: read payment: %w", err)
	}
	return toPayment(row), nil
}

// SettlePayment moves a payment to succeeded exactly once.
//
// The statement's WHERE clause is the gate. A zero-row result is reported as "not
// applied" and not as an error, because it is the normal outcome for ninety-nine of a
// hundred identical deliveries — it is the answer, not a failure.
func (s *Store) SettlePayment(ctx context.Context, paymentID uuid.UUID, paidAt time.Time) (commerce.Payment, bool, error) {
	row, err := s.queries.SettlePayment(ctx, sqlcgen.SettlePaymentParams{
		ID:     paymentID,
		PaidAt: pgtype.Timestamptz{Time: paidAt, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Payment{}, false, nil
		}
		return commerce.Payment{}, false, fmt.Errorf("commercestore: settle payment: %w", err)
	}
	return toPayment(row), true, nil
}

func (s *Store) FailPayment(ctx context.Context, paymentID uuid.UUID, at time.Time) (commerce.Payment, bool, error) {
	row, err := s.queries.FailPayment(ctx, sqlcgen.FailPaymentParams{
		ID:        paymentID,
		UpdatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Payment{}, false, nil
		}
		return commerce.Payment{}, false, fmt.Errorf("commercestore: fail payment: %w", err)
	}
	return toPayment(row), true, nil
}

func (s *Store) AppendGatewayPayload(ctx context.Context, paymentID uuid.UUID, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("commercestore: encode the payload: %w", err)
	}
	if err := s.queries.AppendPaymentPayload(ctx, sqlcgen.AppendPaymentPayloadParams{
		ID:      paymentID,
		Column2: encoded,
	}); err != nil {
		return fmt.Errorf("commercestore: append the payload: %w", err)
	}
	return nil
}

// -------------------------------------------------------------------- outbox --

func (s *Store) RecordOutboxEvent(ctx context.Context, event commerce.OutboxEvent) error {
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("commercestore: encode the event: %w", err)
	}
	if err := s.queries.CreateOutboxEvent(ctx, sqlcgen.CreateOutboxEventParams{
		ID:            event.ID,
		EventType:     event.EventType,
		AggregateType: event.AggregateType,
		AggregateID:   event.AggregateID,
		Payload:       encoded,
	}); err != nil {
		return fmt.Errorf("commercestore: record the event: %w", err)
	}
	return nil
}

// ------------------------------------------------------------------- errors --

// mapWriteError turns a uniqueness violation into the domain's conflict.
//
// It sits on the success path of every write, so nil goes in nil's place: wrapping a
// nil error would report every successful insert as a failure, and the transaction
// around it would roll back work that had actually succeeded.
func mapWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		// A payment start is keyed on a derivation, so a collision means the same order
		// was started twice through the same gateway on the same day — which is what
		// the caller is being told.
		if pgErr.ConstraintName == "payments_idempotency_key_key" {
			return commerce.ErrPaymentAlreadyStarted
		}
		// An order, payment or invoice number colliding is possible in principle and
		// has never happened in practice; it is still a conflict, and still the
		// caller's problem to retry rather than the operator's to diagnose.
		return commerce.ErrConflict
	}
	return fmt.Errorf("commercestore: write failed: %w", err)
}

// ----------------------------------------------------------------- mappings --

// toMoney converts a stored amount. The currency column carries a CHECK constraint of
// the same shape the money package enforces, so the conversion is valid by construction,
// and validating it again here would be re-checking the database on every row.
func toMoney(minor int64, currency string) money.Money {
	return money.Money{AmountMinor: minor, Currency: money.Currency(currency)}
}

// toNames decodes an i18n column. The column is constrained to be an object, so a value
// that does not decode into names is a row this code did not write, and it is reported
// rather than silently reduced to an empty map.
func toNames(raw []byte, column string) (map[string]string, error) {
	var names map[string]string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("commercestore: decode %s: %w", column, err)
	}
	if names == nil {
		names = map[string]string{}
	}
	return names, nil
}

func toProduct(row sqlcgen.Product) (commerce.Product, error) {
	names, err := toNames(row.NameI18n, "products.name_i18n")
	if err != nil {
		return commerce.Product{}, err
	}
	return commerce.Product{
		ID:     row.ID,
		Slug:   row.Slug,
		Name:   names,
		Status: row.Status,
	}, nil
}

// planRow is the shape both plan queries return. Every field of the generated rows is
// present, in the same order, so Go's struct conversion applies to either of them and
// one mapper serves both — and if a column is added to the query, the conversion stops
// compiling rather than silently losing the field.
type planRow struct {
	ID             uuid.UUID
	ProductID      uuid.UUID
	NodeGroupID    *uuid.UUID
	Slug           string
	NameI18n       []byte
	Status         string
	MemoryMb       int32
	DiskGb         int32
	TrafficGb      pgtype.Int8
	BandwidthMbps  pgtype.Int4
	Ipv4Count      int32
	Ipv6Count      int32
	NatPortCount   int32
	Virtualization string
	BillingCycle   string
	PriceMinor     int64
	Currency       string
	StockMode      string
	CreatedAt      pgtype.Timestamptz
	UpdatedAt      pgtype.Timestamptz
	CpuCoresText   string
}

func toPlan(row planRow) (commerce.Plan, error) {
	names, err := toNames(row.NameI18n, "plans.name_i18n")
	if err != nil {
		return commerce.Plan{}, err
	}
	return commerce.Plan{
		ID:             row.ID,
		ProductID:      row.ProductID,
		Slug:           row.Slug,
		Name:           names,
		Status:         row.Status,
		CpuCores:       row.CpuCoresText,
		MemoryMB:       int(row.MemoryMb),
		DiskGB:         int(row.DiskGb),
		Virtualization: row.Virtualization,
		BillingCycle:   row.BillingCycle,
		Price:          toMoney(row.PriceMinor, row.Currency),
	}, nil
}

func toOrder(row sqlcgen.Order, items []commerce.OrderItem) (commerce.Order, error) {
	currency := money.Currency(row.Currency)
	return commerce.Order{
		ID:        row.ID,
		OrderNo:   row.OrderNo,
		UserID:    row.UserID,
		Status:    row.Status,
		Subtotal:  money.Money{AmountMinor: row.SubtotalMinor, Currency: currency},
		Discount:  money.Money{AmountMinor: row.DiscountMinor, Currency: currency},
		Total:     money.Money{AmountMinor: row.TotalMinor, Currency: currency},
		Items:     items,
		PaidAt:    timeOrNil(row.PaidAt),
		CreatedAt: row.CreatedAt.Time.UTC(),
	}, nil
}

func toOrderItem(row sqlcgen.OrderItem) (commerce.OrderItem, error) {
	var snapshot commerce.PlanSnapshot
	if err := json.Unmarshal(row.PlanSnapshot, &snapshot); err != nil {
		return commerce.OrderItem{}, fmt.Errorf("commercestore: decode the plan snapshot: %w", err)
	}
	// The unit price is read from the row and the snapshot is what the order agreed to.
	// The snapshot's currency is what the price is denominated in, because that is the
	// currency the order was taken in.
	return commerce.OrderItem{
		ID:           row.ID,
		ProductID:    row.ProductID,
		PlanID:       row.PlanID,
		Quantity:     int(row.Quantity),
		UnitPrice:    toMoney(row.UnitPriceMinor, snapshot.Currency),
		PlanSnapshot: snapshot,
	}, nil
}

func toPayment(row sqlcgen.Payment) commerce.Payment {
	payload := map[string]any{}
	// A payload that does not decode is carried as empty rather than dropped with its
	// bytes: the row still holds them, and this is a read path, not the record.
	_ = json.Unmarshal(row.GatewayPayload, &payload)

	return commerce.Payment{
		ID:               row.ID,
		PaymentNo:        row.PaymentNo,
		OrderID:          row.OrderID,
		Gateway:          row.Gateway,
		GatewayPaymentID: textValue(row.GatewayPaymentID),
		Status:           row.Status,
		Amount:           toMoney(row.AmountMinor, row.Currency),
		IdempotencyKey:   row.IdempotencyKey,
		PaidAt:           timeOrNil(row.PaidAt),
	}
}

// ------------------------------------------------------------ ledger adapter --

// ledgerStore writes transactions and reads balances, over the same connection as the
// commerce statements. It is unexported: it is not a second store to construct, it is
// what Poster hands the ledger package.
type ledgerStore struct{ queries *sqlcgen.Queries }

func (l *ledgerStore) PostTransaction(ctx context.Context, tx ledger.Transaction) error {
	var referenceID *uuid.UUID
	if tx.ReferenceID != uuid.Nil {
		referenceID = &tx.ReferenceID
	}

	if err := l.queries.CreateLedgerTransaction(ctx, sqlcgen.CreateLedgerTransactionParams{
		ID:            tx.ID,
		Type:          tx.Type,
		ReferenceType: nullableText(tx.ReferenceType),
		ReferenceID:   referenceID,
		Description:   nullableText(tx.Description),
	}); err != nil {
		return fmt.Errorf("commercestore: record the ledger transaction: %w", err)
	}

	for i := range tx.Entries {
		entry := tx.Entries[i]
		if err := l.queries.CreateLedgerEntry(ctx, sqlcgen.CreateLedgerEntryParams{
			ID:            uuid.New(),
			TransactionID: tx.ID,
			AccountType:   entry.AccountType,
			AccountID:     entry.AccountID,
			Direction:     entry.Direction,
			AmountMinor:   entry.Amount.AmountMinor,
			Currency:      string(entry.Amount.Currency),
		}); err != nil {
			// The unique index on the transaction's reference is the structural half of
			// the idempotency decision (ADR-005). Reaching it means the conditional
			// state transition was bypassed, so the failure names the constraint rather
			// than being flattened into a generic write error.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return fmt.Errorf("commercestore: the ledger already holds a %s for %s %s (%s): %w",
					tx.Type, tx.ReferenceType, tx.ReferenceID, pgErr.ConstraintName, err)
			}
			return fmt.Errorf("commercestore: record entry %d: %w", i, err)
		}
	}
	return nil
}

func (l *ledgerStore) SumAccount(ctx context.Context, accountType string, accountID uuid.UUID, currency money.Currency) (int64, error) {
	total, err := l.queries.SumLedgerAccount(ctx, sqlcgen.SumLedgerAccountParams{
		AccountType: accountType,
		AccountID:   accountID,
		Currency:    string(currency),
	})
	if err != nil {
		return 0, fmt.Errorf("commercestore: sum the account: %w", err)
	}
	return total, nil
}

// ------------------------------------------------------------ wallet projection --

// walletProjection maintains the balance column from the entries.
//
// A `user_wallet` account is identified by the customer's identifier, which is what
// makes the projection an upsert by owner and currency rather than a lookup: a wallet
// that does not exist yet is created by the first entry that concerns it.
//
// Entries for any other account type are not its business, and it leaves them alone.
type walletProjection struct{ queries *sqlcgen.Queries }

func (w *walletProjection) Apply(ctx context.Context, entries []ledger.Entry) error {
	for i := range entries {
		entry := entries[i]
		if entry.AccountType != ledger.AccountUserWallet {
			continue
		}
		if err := w.queries.UpsertWalletBalance(ctx, sqlcgen.UpsertWalletBalanceParams{
			// The identifier is unused on the conflict path, where the wallet already
			// exists and keeps its own.
			ID:                    uuid.New(),
			UserID:                entry.AccountID,
			Currency:              string(entry.Amount.Currency),
			AvailableBalanceMinor: entry.Signed(),
		}); err != nil {
			return fmt.Errorf("commercestore: apply the wallet delta for %s: %w", entry.AccountID, err)
		}
	}
	return nil
}

// ------------------------------------------------------------------- helpers --

func nullableTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func timeOrNil(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	value.Time = value.Time.UTC()
	return &value.Time
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

package commercestore

// The subscription half of the commerce adapter.
//
// The mapping rules are the ones commerce.go already established: timestamps come
// in and out as pgtype with UTC normalization, i18n columns are decoded once and
// a value that does not decode is reported rather than silently emptied, and
// every conditional update's zero-row result is the caller's answer rather than
// an error — for a sweep, losing a race is the normal outcome.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// toSubscription converts one subscriptions row into the domain's shape.
func toSubscription(row sqlcgen.Subscription) commerce.Subscription {
	subscription := commerce.Subscription{
		ID:                 row.ID,
		UserID:             row.UserID,
		PlanID:             row.PlanID,
		Status:             row.Status,
		BillingCycle:       row.BillingCycle,
		Price:              toMoney(row.PriceMinor, row.Currency),
		CurrentPeriodStart: row.CurrentPeriodStart.Time.UTC(),
		CurrentPeriodEnd:   row.CurrentPeriodEnd.Time.UTC(),
		NextDueAt:          row.NextDueAt.Time.UTC(),
		CancelAtPeriodEnd:  row.CancelAtPeriodEnd,
		Version:            row.Version,
	}
	if row.StartedAt.Valid {
		value := row.StartedAt.Time.UTC()
		subscription.StartedAt = &value
	}
	if row.GraceUntil.Valid {
		value := row.GraceUntil.Time.UTC()
		subscription.GraceUntil = &value
	}
	if row.EndedAt.Valid {
		value := row.EndedAt.Time.UTC()
		subscription.EndedAt = &value
	}
	return subscription
}

// toInvoice converts one invoices row. The origin is whatever the row carries —
// an order, a subscription, or neither of them is a row this code did not write,
// and the schema's CHECK is the thing that said so on the way in.
func toInvoice(row sqlcgen.Invoice) commerce.Invoice {
	invoice := commerce.Invoice{
		ID:        row.ID,
		InvoiceNo: row.InvoiceNo,
		UserID:    row.UserID,
		Status:    row.Status,
		Amount:    toMoney(row.AmountMinor, row.Currency),
	}
	if row.OrderID != nil {
		orderID := *row.OrderID
		invoice.OrderID = &orderID
	}
	if row.SubscriptionID != nil {
		subscriptionID := *row.SubscriptionID
		invoice.SubscriptionID = &subscriptionID
	}
	if row.DueAt.Valid {
		value := row.DueAt.Time.UTC()
		invoice.DueAt = &value
	}
	return invoice
}

// CreateSubscription persists a newly settled subscription.
func (s *Store) CreateSubscription(ctx context.Context, sub commerce.Subscription) error {
	return mapWriteError(s.queries.CreateSubscription(ctx, sqlcgen.CreateSubscriptionParams{
		ID:                 sub.ID,
		UserID:             sub.UserID,
		PlanID:             sub.PlanID,
		Status:             sub.Status,
		BillingCycle:       sub.BillingCycle,
		PriceMinor:         sub.Price.AmountMinor,
		Currency:           string(sub.Price.Currency),
		StartedAt:          nullableTime(sub.StartedAt),
		CurrentPeriodStart: pgtype.Timestamptz{Time: sub.CurrentPeriodStart, Valid: true},
		CurrentPeriodEnd:   pgtype.Timestamptz{Time: sub.CurrentPeriodEnd, Valid: true},
		NextDueAt:          pgtype.Timestamptz{Time: sub.NextDueAt, Valid: true},
	}))
}

// SubscriptionByID reads one subscription without the owner scope: the
// administrator's terminate goes through here, and the settlement's own reads do
// not know the customer's session.
func (s *Store) SubscriptionByID(ctx context.Context, id uuid.UUID) (commerce.Subscription, error) {
	row, err := s.queries.SubscriptionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Subscription{}, commerce.ErrSubscriptionNotFound
		}
		return commerce.Subscription{}, fmt.Errorf("commercestore: read subscription: %w", err)
	}
	return toSubscription(row), nil
}

// SubscriptionForUser reads one subscription scoped to its owner, so someone
// else's is indistinguishable from none.
func (s *Store) SubscriptionForUser(ctx context.Context, id, userID uuid.UUID) (commerce.Subscription, error) {
	row, err := s.queries.SubscriptionByIDForUser(ctx, sqlcgen.SubscriptionByIDForUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Subscription{}, commerce.ErrSubscriptionNotFound
		}
		return commerce.Subscription{}, fmt.Errorf("commercestore: read subscription: %w", err)
	}
	return toSubscription(row), nil
}

// ListSubscriptionsForUser returns a customer's subscriptions, newest first.
func (s *Store) ListSubscriptionsForUser(ctx context.Context, userID uuid.UUID) ([]commerce.Subscription, error) {
	rows, err := s.queries.ListSubscriptionsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("commercestore: list subscriptions: %w", err)
	}
	subscriptions := make([]commerce.Subscription, 0, len(rows))
	for i := range rows {
		subscriptions = append(subscriptions, toSubscription(rows[i]))
	}
	return subscriptions, nil
}

// SubscriptionsPastDeadline is the sweep's work list, with the plan's words for
// the renewal invoice's line.
func (s *Store) SubscriptionsPastDeadline(ctx context.Context, now time.Time) ([]commerce.SweepSubscription, error) {
	rows, err := s.queries.SubscriptionsPastDeadline(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("commercestore: read the sweep's work list: %w", err)
	}
	entries := make([]commerce.SweepSubscription, 0, len(rows))
	for i := range rows {
		row := rows[i]
		names, err := toNames(row.PlanNameI18n, "plans.name_i18n")
		if err != nil {
			return nil, err
		}
		entries = append(entries, commerce.SweepSubscription{
			Subscription: toSubscription(sqlcgen.Subscription{
				ID:                 row.ID,
				UserID:             row.UserID,
				PlanID:             row.PlanID,
				Status:             row.Status,
				BillingCycle:       row.BillingCycle,
				PriceMinor:         row.PriceMinor,
				Currency:           row.Currency,
				StartedAt:          row.StartedAt,
				CurrentPeriodStart: row.CurrentPeriodStart,
				CurrentPeriodEnd:   row.CurrentPeriodEnd,
				NextDueAt:          row.NextDueAt,
				GraceUntil:         row.GraceUntil,
				CancelAtPeriodEnd:  row.CancelAtPeriodEnd,
				EndedAt:            row.EndedAt,
				Version:            row.Version,
				CreatedAt:          row.CreatedAt,
				UpdatedAt:          row.UpdatedAt,
			}),
			PlanSlug: row.PlanSlug,
			PlanName: names,
		})
	}
	return entries, nil
}

// TransitionSubscription moves the row from the state the caller saw. The
// cancellation flag is the statement's business (see subscription.sql): reaching
// `cancelled` clears it, every other target leaves it as the customer set it.
func (s *Store) TransitionSubscription(ctx context.Context, id uuid.UUID, from, to string,
	deadline, endedAt *time.Time, at time.Time) (bool, error) {
	tag, err := s.queries.TransitionSubscription(ctx, sqlcgen.TransitionSubscriptionParams{
		ID:         id,
		Status:     to,
		GraceUntil: nullableTime(deadline),
		EndedAt:    nullableTime(endedAt),
		UpdatedAt:  pgtype.Timestamptz{Time: at, Valid: true},
		Status_2:   from,
	})
	if err != nil {
		return false, fmt.Errorf("commercestore: transition the subscription: %w", err)
	}
	return tag == 1, nil
}

// ExtendSubscription starts the next period. A zero-row result is the race being
// lost, not a failure.
func (s *Store) ExtendSubscription(ctx context.Context, id uuid.UUID,
	periodStart, periodEnd, at time.Time) (bool, error) {
	tag, err := s.queries.ExtendSubscription(ctx, sqlcgen.ExtendSubscriptionParams{
		ID:                 id,
		CurrentPeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true},
		CurrentPeriodEnd:   pgtype.Timestamptz{Time: periodEnd, Valid: true},
		UpdatedAt:          pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("commercestore: extend the subscription: %w", err)
	}
	return tag == 1, nil
}

// SetCancelAtPeriodEnd records the owner's request, and only from a live state.
func (s *Store) SetCancelAtPeriodEnd(ctx context.Context, id, userID uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.SetCancelAtPeriodEnd(ctx, sqlcgen.SetCancelAtPeriodEndParams{
		ID:        id,
		UserID:    userID,
		UpdatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("commercestore: set the cancellation flag: %w", err)
	}
	return tag == 1, nil
}

// OrderForSettlement reads an order with its lines, without the owner scope: the
// settlement acts on the platform's own record.
func (s *Store) OrderForSettlement(ctx context.Context, orderID uuid.UUID) (commerce.Order, error) {
	row, err := s.queries.OrderByIDForSettlement(ctx, orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Order{}, commerce.ErrOrderNotFound
		}
		return commerce.Order{}, fmt.Errorf("commercestore: read the settled order: %w", err)
	}
	items, err := s.orderItems(ctx, orderID)
	if err != nil {
		return commerce.Order{}, err
	}
	return toOrder(row, items)
}

// CreateSubscriptionInvoice opens a renewal invoice. A unique violation on the
// open-renewal index is a lost race between sweeps, and it reaches the caller as
// the domain's conflict — the same vocabulary every other write loses with.
func (s *Store) CreateSubscriptionInvoice(ctx context.Context, invoice commerce.Invoice) error {
	if err := s.queries.CreateSubscriptionInvoice(ctx, sqlcgen.CreateSubscriptionInvoiceParams{
		ID:             invoice.ID,
		InvoiceNo:      invoice.InvoiceNo,
		UserID:         invoice.UserID,
		SubscriptionID: invoice.SubscriptionID,
		Status:         invoice.Status,
		AmountMinor:    invoice.Amount.AmountMinor,
		Currency:       string(invoice.Amount.Currency),
		DueAt:          nullableTime(invoice.DueAt),
	}); err != nil {
		return mapWriteError(err)
	}
	return nil
}

// CreateSubscriptionInvoiceItems writes the renewal invoice's lines. They are the
// same lines any invoice carries, so the same writer serves both.
func (s *Store) CreateSubscriptionInvoiceItems(ctx context.Context, invoiceID uuid.UUID, items []commerce.InvoiceItem) error {
	return s.CreateInvoiceItems(ctx, invoiceID, items)
}

// OpenRenewalInvoiceFor reads back the invoice a lost race left open.
func (s *Store) OpenRenewalInvoiceFor(ctx context.Context, subscriptionID uuid.UUID) (commerce.Invoice, error) {
	row, err := s.queries.OpenInvoiceForSubscription(ctx, &subscriptionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Invoice{}, commerce.ErrInvoiceNotFound
		}
		return commerce.Invoice{}, fmt.Errorf("commercestore: read the open renewal invoice: %w", err)
	}
	return toInvoice(row), nil
}

// MarkSubscriptionInvoicePaid is the renewal's settlement gate.
func (s *Store) MarkSubscriptionInvoicePaid(ctx context.Context, invoiceID uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.MarkSubscriptionInvoicePaid(ctx, sqlcgen.MarkSubscriptionInvoicePaidParams{
		ID:     invoiceID,
		PaidAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("commercestore: settle the renewal invoice: %w", err)
	}
	return tag == 1, nil
}

// LockWalletForSpend locks the wallet row and reports the balance it holds. A
// wallet that does not exist has nothing to spend, and that is an empty balance
// rather than an error: it is the normal state of a customer who has never been
// credited.
func (s *Store) LockWalletForSpend(ctx context.Context, userID uuid.UUID, currency money.Currency) (money.Money, error) {
	row, err := s.queries.LockWalletForSpend(ctx, sqlcgen.LockWalletForSpendParams{
		UserID:   userID,
		Currency: string(currency),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return money.Money{Currency: currency}, nil
		}
		return money.Money{}, fmt.Errorf("commercestore: lock the wallet: %w", err)
	}
	return toMoney(row.AvailableBalanceMinor, row.Currency), nil
}

package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/ledger"
)

// The subscription service: the machine's writers.
//
// They live beside the order and payment writers in the same package for the
// reason ADR-006 §8 records — activation happens inside the settlement
// transaction, and renewal opens invoices and moves ledger money — while the
// records themselves stay separate tables, as AGENTS.md requires.

// Aggregate type for the subscription's outbox events.
const AggregateSubscription = "subscription"

// Versioned event types (docs/09). One per machine transition.
const (
	EventSubscriptionActivated = "subscription.activated.v1"
	EventSubscriptionRenewed   = "subscription.renewed.v1"
	EventSubscriptionPastDue   = "subscription.past_due.v1"
	EventSubscriptionSuspended = "subscription.suspended.v1"
	EventSubscriptionExpired   = "subscription.expired.v1"
	EventSubscriptionCancelled = "subscription.cancelled.v1"
	EventSubscriptionTmprd     = "subscription.terminated.v1"
)

// SweepSubscription is one row of the sweep's work list: the subscription plus
// the plan's words, which the renewal invoice's line speaks in.
type SweepSubscription struct {
	Subscription
	PlanSlug string
	PlanName map[string]string
}

// SweepResult is what one sweep pass did, in counts an operator can check
// against the outbox rather than against a log line.
type SweepResult struct {
	Scanned   int
	Renewed   int
	Cancelled int
	PastDue   int
	Suspended int
	Expired   int
	Skipped   int
}

// compile-time check that the full Store keeps carrying the subscription surface.
// activateSubscriptionsFromOrder creates one subscription per order line, inside
// the settlement's transaction (ADR-006 §2). It is unexported: the settlement is
// the only door.
func activateSubscriptionsFromOrder(ctx context.Context, tx Store, order Order, now time.Time) error {
	for i := range order.Items {
		line := order.Items[i]
		snapshot := line.PlanSnapshot
		periodEnd, err := AddBillingCycle(snapshot.BillingCycle, now)
		if err != nil {
			return fmt.Errorf("commerce: period for %q: %w", snapshot.BillingCycle, err)
		}
		started := now.UTC()
		subscription := Subscription{
			ID:                 uuid.New(),
			UserID:             order.UserID,
			PlanID:             line.PlanID,
			Status:             SubscriptionActive,
			BillingCycle:       snapshot.BillingCycle,
			Price:              line.UnitPrice,
			StartedAt:          &started,
			CurrentPeriodStart: started,
			CurrentPeriodEnd:   periodEnd.UTC(),
			NextDueAt:          periodEnd.UTC(),
		}
		if err := tx.CreateSubscription(ctx, subscription); err != nil {
			return fmt.Errorf("commerce: create the subscription: %w", err)
		}
		if err := tx.RecordOutboxEvent(ctx, OutboxEvent{
			ID:            uuid.New(),
			EventType:     EventSubscriptionActivated,
			AggregateType: AggregateSubscription,
			AggregateID:   subscription.ID,
			Payload: map[string]any{
				"subscription_id":    subscription.ID.String(),
				"user_id":            subscription.UserID.String(),
				"plan_id":            subscription.PlanID.String(),
				"billing_cycle":      subscription.BillingCycle,
				"order_id":           order.ID.String(),
				"current_period_end": subscription.CurrentPeriodEnd.Format(time.RFC3339),
			},
		}); err != nil {
			return fmt.Errorf("commerce: record the activation event: %w", err)
		}
	}
	return nil
}

// Sweep runs one pass of the calendar (ADR-006 §4). It is a function of the
// records and `now`: the instant comes in as an argument, so the periodic loop
// that will call it is somebody else's phase, and a test can sweep into next
// month without waiting for it.
//
// Each subscription's transition is its own transaction: one row that cannot be
// processed fails alone, and the rest of the calendar still moves.
func (s *Service) Sweep(ctx context.Context, now time.Time) (SweepResult, error) {
	rows, err := s.store.SubscriptionsPastDeadline(ctx, now)
	if err != nil {
		return SweepResult{}, fmt.Errorf("commerce: read the sweep's work list: %w", err)
	}

	result := SweepResult{Scanned: len(rows)}
	for i := range rows {
		outcome, err := s.sweepOne(ctx, rows[i], now)
		if err != nil {
			return result, fmt.Errorf("commerce: sweep subscription %s: %w", rows[i].ID, err)
		}
		switch outcome {
		case outcomeRenewed:
			result.Renewed++
		case outcomeCancelled:
			result.Cancelled++
		case outcomePastDue:
			result.PastDue++
		case outcomeSuspended:
			result.Suspended++
		case outcomeExpired:
			result.Expired++
		default:
			result.Skipped++
		}
	}
	return result, nil
}

// Outcomes of one subscription's sweep, in the terms the result reports.
const (
	outcomeSkipped   = "skipped"
	outcomeRenewed   = "renewed"
	outcomeCancelled = "cancelled"
	outcomePastDue   = "past_due"
	outcomeSuspended = "suspended"
	outcomeExpired   = "expired"
)

// sweepOne processes one subscription whose deadline has passed. The outcome says
// what was written; racing another sweep means losing the write and reporting a
// skip, which is the answer rather than a failure.
//
// The checks run in the order the machine arrives at its states in: a
// cancellation is honoured before a bill is opened, the bill is opened before the
// payment is attempted, and each regression — past due, suspension, expiry — is
// gated on the state the row is actually in when this pass looks at it.
func (s *Service) sweepOne(ctx context.Context, row SweepSubscription, now time.Time) (string, error) {
	periodEnd := row.CurrentPeriodEnd

	// 1. A cancellation request outlives the period: cancel, and open nothing.
	if row.CancelAtPeriodEnd &&
		(row.Status == SubscriptionActive || row.Status == SubscriptionPastDue) {
		applied, err := s.transitionLive(ctx, row, SubscriptionCancelled, nil, &now, now)
		if err != nil || !applied {
			return outcomeSkipped, err
		}
		return outcomeCancelled, nil
	}

	// 2. The renewal: open the invoice for the period that has ended, then try to
	// pay it. The unique index on open renewal invoices is the arbiter between
	// concurrent sweeps; the invoice's own conditional transition is the arbiter
	// between concurrent payers.
	invoice, err := s.openRenewalInvoice(ctx, row, periodEnd, now)
	if err != nil {
		return outcomeSkipped, err
	}
	paid, err := s.payRenewalInvoice(ctx, row, invoice, now)
	if err != nil {
		return outcomeSkipped, err
	}
	if paid {
		return outcomeRenewed, nil
	}

	// 3. Unpaid at the period's end: past due, with a grace deadline.
	if row.Status == SubscriptionActive {
		deadline := SubscriptionDeadline(SubscriptionPastDue, periodEnd)
		applied, err := s.transitionLive(ctx, row, SubscriptionPastDue, &deadline, nil, now)
		if err != nil || !applied {
			return outcomeSkipped, err
		}
		return outcomePastDue, nil
	}

	// 4. Unpaid past grace: suspended, with an expiry deadline.
	if row.Status == SubscriptionPastDue && now.After(periodEnd.Add(GracePastDue)) {
		deadline := SubscriptionDeadline(SubscriptionSuspended, now)
		applied, err := s.transitionLive(ctx, row, SubscriptionSuspended, &deadline, nil, now)
		if err != nil || !applied {
			return outcomeSkipped, err
		}
		return outcomeSuspended, nil
	}

	// 5. Suspended past its expiry deadline: expired, and the machine stops.
	if row.Status == SubscriptionSuspended && row.GraceUntil != nil && now.After(*row.GraceUntil) {
		applied, err := s.transitionLive(ctx, row, SubscriptionExpired, nil, &now, now)
		if err != nil || !applied {
			return outcomeSkipped, err
		}
		return outcomeExpired, nil
	}

	return outcomeSkipped, nil
}

// transitionEvent maps a target state onto the event that records reaching it.
var transitionEvent = map[string]string{
	SubscriptionPastDue:    EventSubscriptionPastDue,
	SubscriptionSuspended:  EventSubscriptionSuspended,
	SubscriptionExpired:    EventSubscriptionExpired,
	SubscriptionCancelled:  EventSubscriptionCancelled,
	SubscriptionTerminated: EventSubscriptionTmprd,
}

// transitionLive performs one conditional machine move and records its event.
// A false result means another writer moved the row first; the caller reports
// the skip rather than forcing the state.
func (s *Service) transitionLive(ctx context.Context, row SweepSubscription, to string,
	deadline, endedAt *time.Time, now time.Time) (bool, error) {
	applied, err := s.store.TransitionSubscription(ctx, row.ID, row.Status, to,
		deadline, endedAt, now)
	if err != nil || !applied {
		return applied, err
	}
	event := transitionEvent[to]
	if event == "" {
		return true, fmt.Errorf("commerce: transition to %q has no event type", to)
	}
	if err := s.store.RecordOutboxEvent(ctx, OutboxEvent{
		ID:            uuid.New(),
		EventType:     event,
		AggregateType: AggregateSubscription,
		AggregateID:   row.ID,
		Payload: map[string]any{
			"subscription_id": row.ID.String(),
			"user_id":         row.UserID.String(),
			"from":            row.Status,
			"to":              to,
			"at":              now.UTC().Format(time.RFC3339),
		},
	}); err != nil {
		return true, fmt.Errorf("commerce: record the %s event: %w", to, err)
	}
	return true, nil
}

// openRenewalInvoice opens the bill for the period that has ended. Concurrent
// sweeps are arbitrated by the unique index on open renewal invoices: the loser
// reads back the invoice that won and carries on to the payment attempt, so both
// sweeps converge on the same bill instead of each opening their own.
func (s *Service) openRenewalInvoice(ctx context.Context, row SweepSubscription, periodEnd, now time.Time) (Invoice, error) {
	invoiceNo, err := NewInvoiceNumber(now)
	if err != nil {
		return Invoice{}, err
	}
	dueAt := periodEnd.UTC()
	subscriptionID := row.ID
	invoice := Invoice{
		ID:             uuid.New(),
		InvoiceNo:      invoiceNo,
		UserID:         row.UserID,
		SubscriptionID: &subscriptionID,
		Status:         InvoiceOpen,
		Amount:         row.Price,
		DueAt:          &dueAt,
		Items: []InvoiceItem{{
			ID:          uuid.New(),
			Description: row.PlanName,
			Quantity:    1,
			UnitAmount:  row.Price,
			Total:       row.Price,
		}},
	}
	err = s.store.WithinTransaction(ctx, func(tx Store) error {
		if err := tx.CreateSubscriptionInvoice(ctx, invoice); err != nil {
			return fmt.Errorf("commerce: open the renewal invoice: %w", err)
		}
		if err := tx.CreateSubscriptionInvoiceItems(ctx, invoice.ID, invoice.Items); err != nil {
			return fmt.Errorf("commerce: itemise the renewal invoice: %w", err)
		}
		return nil
	})
	if err == nil {
		return invoice, nil
	}
	if !errors.Is(err, ErrConflict) {
		return Invoice{}, err
	}
	// Lost the race to another sweep. The bill that won is the one to pay.
	existing, err := s.store.OpenRenewalInvoiceFor(ctx, row.ID)
	if err != nil {
		return Invoice{}, fmt.Errorf("commerce: read back the open renewal invoice: %w", err)
	}
	return existing, nil
}

// Internal outcomes of the renewal transaction. Unexported: the service maps them
// to results, and no caller outside this package can see them.
var (
	errBalanceShort  = errors.New("commerce: the wallet could not back the renewal")
	errExtensionLost = errors.New("commerce: the extension raced another writer")
)

// payRenewalInvoice tries to settle the renewal from the wallet and, on success,
// extends the period — all inside one transaction, so the money and the service
// move together (ADR-006 §6).
func (s *Service) payRenewalInvoice(ctx context.Context, row SweepSubscription, invoice Invoice, now time.Time) (bool, error) {
	paid := false
	err := s.store.WithinTransaction(ctx, func(tx Store) error {
		// The gate. A zero-row result means another payer got there first — or
		// that the invoice is not open at all, which for a renewal that was
		// already paid is the same answer.
		applied, err := tx.MarkSubscriptionInvoicePaid(ctx, invoice.ID, now)
		if err != nil {
			return fmt.Errorf("commerce: settle the renewal invoice: %w", err)
		}
		if !applied {
			return nil
		}

		// The money: out of the customer's wallet, into revenue. The wallet row
		// is locked, so a wallet that backs one renewal cannot back two — the
		// second of two concurrent renewals reads the balance the first left,
		// finds it short, and rolls this whole transaction back, leaving the
		// invoice open for the next pass. The debit itself is the ledger entry
		// below, whose projection delta is applied exactly once.
		balance, err := tx.LockWalletForSpend(ctx, row.UserID, invoice.Amount.Currency)
		if err != nil {
			return fmt.Errorf("commerce: read the wallet: %w", err)
		}
		if balance.AmountMinor < invoice.Amount.AmountMinor {
			return errBalanceShort
		}

		if err := tx.Poster().Post(ctx, ledger.Transaction{
			ID:            uuid.New(),
			Type:          ledger.TypePaymentSettlement,
			ReferenceType: "invoice",
			ReferenceID:   invoice.ID,
			Description:   "renewal invoice " + invoice.InvoiceNo,
			Entries: []ledger.Entry{
				{
					AccountType: ledger.AccountUserWallet,
					AccountID:   row.UserID,
					Direction:   ledger.DirectionDebit,
					Amount:      invoice.Amount,
				},
				{
					AccountType: ledger.AccountRevenue,
					AccountID:   ledger.AccountIDRevenue,
					Direction:   ledger.DirectionCredit,
					Amount:      invoice.Amount,
				},
			},
		}); err != nil {
			return fmt.Errorf("commerce: post the renewal movement: %w", err)
		}

		// The service: the next period starts where the last one ended, whatever
		// the sweep's `now` says — a late renewal pays for the service it missed,
		// not for the day it finally paid.
		periodEnd, err := AddBillingCycle(row.BillingCycle, row.CurrentPeriodEnd)
		if err != nil {
			return fmt.Errorf("commerce: extend the period: %w", err)
		}
		extended, err := tx.ExtendSubscription(ctx, row.ID, row.CurrentPeriodEnd, periodEnd, now)
		if err != nil {
			return fmt.Errorf("commerce: extend the subscription: %w", err)
		}
		if !extended {
			// Another writer moved the row out from under this payment — a
			// concurrent sweep that terminated it, most likely. The whole
			// transaction rolls back: the money moves only when the service does.
			return errExtensionLost
		}

		if err := tx.RecordOutboxEvent(ctx, OutboxEvent{
			ID:            uuid.New(),
			EventType:     EventSubscriptionRenewed,
			AggregateType: AggregateSubscription,
			AggregateID:   row.ID,
			Payload: map[string]any{
				"subscription_id":    row.ID.String(),
				"user_id":            row.UserID.String(),
				"invoice_id":         invoice.ID.String(),
				"amount_minor":       invoice.Amount.AmountMinor,
				"currency":           string(invoice.Amount.Currency),
				"current_period_end": periodEnd.UTC().Format(time.RFC3339),
			},
		}); err != nil {
			return fmt.Errorf("commerce: record the renewal event: %w", err)
		}
		paid = true
		return nil
	})
	if errors.Is(err, errBalanceShort) || errors.Is(err, errExtensionLost) {
		// Not errors: an empty wallet is why past_due exists, and a lost
		// extension means another writer was faster. The transaction rolled
		// back; the next pass tries again.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return paid, nil
}

// RequestCancellation records the customer's cancellation request. It takes
// effect when the period ends (ADR-006 §7); the current period has been paid for.
func (s *Service) RequestCancellation(ctx context.Context, userID, subscriptionID uuid.UUID, now time.Time) error {
	subscription, err := s.store.SubscriptionForUser(ctx, subscriptionID, userID)
	if err != nil {
		return err
	}
	if !SubscriptionIsLive(subscription.Status) {
		return fmt.Errorf("%w: it is %s", ErrSubscriptionNotLive, subscription.Status)
	}
	applied, err := s.store.SetCancelAtPeriodEnd(ctx, subscriptionID, userID, now)
	if err != nil {
		return err
	}
	if !applied {
		return ErrSubscriptionNotLive
	}
	return nil
}

// RenewNow pays a due renewal immediately, on the customer's request, from the
// wallet. It is the manual half of the sweep's automatic pass, and it reaches the
// same transaction — the same invoice, the same gate, the same extension.
func (s *Service) RenewNow(ctx context.Context, userID, subscriptionID uuid.UUID, now time.Time) (Subscription, error) {
	subscription, err := s.store.SubscriptionForUser(ctx, subscriptionID, userID)
	if err != nil {
		return Subscription{}, err
	}
	if !SubscriptionIsLive(subscription.Status) {
		return Subscription{}, fmt.Errorf("%w: it is %s", ErrSubscriptionNotLive, subscription.Status)
	}

	// What is there to pay? If the period has ended, this opens the renewal bill
	// — the same door the sweep uses, so both paths converge on one invoice. If
	// it has not, the only payable thing is a bill the sweep already opened: a
	// customer settling a past-due renewal early, before the next pass runs.
	var invoice Invoice
	if !now.Before(subscription.NextDueAt) {
		row := SweepSubscription{Subscription: subscription}
		// The renewal invoice's line speaks in the plan's words, as every
		// invoice line does — including the one this request might open.
		plan, planErr := s.store.PlanByID(ctx, subscription.PlanID)
		if planErr == nil {
			row.PlanName = plan.Name
			row.PlanSlug = plan.Slug
		}
		invoice, err = s.openRenewalInvoice(ctx, row, subscription.CurrentPeriodEnd, now)
		if err != nil {
			return Subscription{}, err
		}
	} else {
		invoice, err = s.store.OpenRenewalInvoiceFor(ctx, subscriptionID)
		if errors.Is(err, ErrInvoiceNotFound) {
			return Subscription{}, fmt.Errorf("%w: it is due at %s",
				ErrNothingDue, subscription.NextDueAt.Format(time.RFC3339))
		}
		if err != nil {
			return Subscription{}, err
		}
	}

	row := SweepSubscription{Subscription: subscription}
	paid, err := s.payRenewalInvoice(ctx, row, invoice, now)
	if err != nil {
		return Subscription{}, err
	}
	if !paid {
		// The invoice is open and the wallet could not cover it; the caller sees
		// the balance error rather than a silent nothing.
		return Subscription{}, ErrInsufficientBalance
	}
	return s.store.SubscriptionForUser(ctx, subscriptionID, userID)
}

// Terminate ends a subscription on an administrator's authority. The customer has
// no route here: `terminated` answers abuse and chargebacks, not preference
// (ADR-006 §7).
func (s *Service) Terminate(ctx context.Context, subscriptionID uuid.UUID, now time.Time) error {
	subscription, err := s.store.SubscriptionByID(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if !SubscriptionIsLive(subscription.Status) {
		return fmt.Errorf("%w: it is %s", ErrSubscriptionNotLive, subscription.Status)
	}
	row := SweepSubscription{Subscription: subscription, PlanName: map[string]string{}}
	applied, err := s.transitionLive(ctx, row, SubscriptionTerminated, nil, &now, now)
	if err != nil {
		return err
	}
	if !applied {
		return ErrSubscriptionNotLive
	}
	return nil
}

// ListSubscriptionsForUser returns a customer's subscriptions.
func (s *Service) ListSubscriptionsForUser(ctx context.Context, userID uuid.UUID) ([]Subscription, error) {
	return s.store.ListSubscriptionsForUser(ctx, userID)
}

// GetSubscription returns one of a customer's subscriptions. The owner is part of
// the lookup, so someone else's subscription is indistinguishable from none.
func (s *Service) GetSubscription(ctx context.Context, userID, id uuid.UUID) (Subscription, error) {
	return s.store.SubscriptionForUser(ctx, id, userID)
}

package commerce

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// The subscription machine.
//
// `docs/05` fixes the vocabulary; the edges are this repository's decision
// (ADR-006), stated here in the same table-and-test form as the order and payment
// machines so the test that reads the document holds the vocabulary to it.

// Subscription statuses.
const (
	SubscriptionPending    = "pending"
	SubscriptionActive     = "active"
	SubscriptionPastDue    = "past_due"
	SubscriptionSuspended  = "suspended"
	SubscriptionCancelled  = "cancelled"
	SubscriptionExpired    = "expired"
	SubscriptionTerminated = "terminated"
)

// subscriptionTransitions is the machine ADR-006 fixes.
//
// The entry states have no source row in this table: a subscription is born
// active from a settled order (ADR-006 §2), so `pending` — which the reference
// declares and the column's CHECK permits — is a state no writer reaches yet, and
// it is deliberately absent rather than wired to a path nobody asked for.
var subscriptionTransitions = map[string][]string{
	SubscriptionActive: {
		// The period ended and the renewal invoice is unpaid.
		SubscriptionPastDue,
		// cancel_at_period_end was set and the period has ended.
		SubscriptionCancelled,
		SubscriptionTerminated,
	},
	SubscriptionPastDue: {
		// The renewal was paid inside the grace window.
		SubscriptionActive,
		SubscriptionSuspended,
		SubscriptionCancelled,
		SubscriptionTerminated,
	},
	SubscriptionSuspended: {
		// Recovery: docs/07's renew chain ends "if suspended set desired running".
		SubscriptionActive,
		// The expiry window passed with nothing paid.
		SubscriptionExpired,
		SubscriptionTerminated,
	},
}

// SubscriptionCanTransition reports whether the machine allows the move.
func SubscriptionCanTransition(from, to string) bool {
	for _, next := range subscriptionTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// SubscriptionStatuses returns every status the machine knows, in a fixed order.
// The vocabulary test reads it against docs/05's list.
func SubscriptionStatuses() []string {
	return []string{
		SubscriptionPending,
		SubscriptionActive,
		SubscriptionPastDue,
		SubscriptionSuspended,
		SubscriptionCancelled,
		SubscriptionExpired,
		SubscriptionTerminated,
	}
}

// The sweep's policy (ADR-006 §5). A renewal window is a product decision, so the
// constants live beside the machine rather than in a column the schema would
// guess at per plan.
const (
	// GracePastDue is how long past `current_period_end` an unpaid renewal keeps
	// the subscription in `past_due` before suspension.
	GracePastDue = 72 * time.Hour
	// ExpiryAfterSuspension is how long a suspended subscription waits before the
	// machine gives up on payment and expires it.
	ExpiryAfterSuspension = 14 * 24 * time.Hour
)

// SubscriptionDeadline puts one meaning on `grace_until`: the instant by which
// the row's current state must be resolved. While past due that is the grace
// deadline; once suspended it is the expiry deadline. The sweep reads it to know
// when a state has run out of time, and the extension clears it, because an
// active row has nothing to resolve.
func SubscriptionDeadline(status string, at time.Time) time.Time {
	switch status {
	case SubscriptionPastDue:
		return at.Add(GracePastDue)
	case SubscriptionSuspended:
		return at.Add(ExpiryAfterSuspension)
	default:
		return time.Time{}
	}
}

// AddBillingCycle moves a period boundary forward by one cycle.
//
// Calendar months, not fixed days, and clamped to the month's end: a subscription
// that started on the 31st renews on the 28th of February rather than drifting
// into March — Go's own AddDate normalises the overflow (Jan 31 becomes Mar 3),
// which would move the anniversary instead of shortening the month. The billing
// cycle is the plan's column and the CHECK in 0004 names the values; a value
// outside them is a row this code did not write, and it is refused rather than
// silently treated as a month.
func AddBillingCycle(cycle string, from time.Time) (time.Time, error) {
	months := 0
	switch cycle {
	case "monthly":
		months = 1
	case "quarterly":
		months = 3
	case "semiannually":
		months = 6
	case "annually":
		months = 12
	default:
		return time.Time{}, fmt.Errorf("%w: %q", ErrUnknownBillingCycle, cycle)
	}

	// The intended month, computed from the start so the overflow AddDate would
	// introduce cannot move it.
	total := int(from.Month()) - 1 + months
	year := from.Year() + total/12
	month := time.Month(total%12 + 1)

	// The day, clamped to the month's last day. The clock — and the location,
	// which is UTC for every period boundary this code writes — come from `from`.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, from.Location()).Day()
	day := from.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day,
		from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), from.Location()), nil
}

// Subscription is one customer's ongoing entitlement to a plan.
type Subscription struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	PlanID       uuid.UUID
	Status       string
	BillingCycle string
	Price        money.Money
	StartedAt    *time.Time
	// CurrentPeriod is the span of service the customer has paid for. Both ends
	// are set from the moment the subscription is born (ADR-006 §2), so a nil
	// here is a row this code did not write.
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	// NextDueAt is when the next renewal invoice becomes payable. It equals the
	// period's end: the customer's service and their bill end together.
	NextDueAt time.Time
	// GraceUntil is the state's resolution deadline (see subscriptionDeadline).
	GraceUntil *time.Time
	// CancelAtPeriodEnd is the customer's cancellation request. The sweep honours
	// it when the period ends, before it opens the next invoice.
	CancelAtPeriodEnd bool
	EndedAt           *time.Time
	Version           int64
}

// SubscriptionIsLive reports whether the subscription can still change on its own.
// Terminal states are terminal in the sweep as well as in the machine.
func SubscriptionIsLive(status string) bool {
	switch status {
	case SubscriptionActive, SubscriptionPastDue, SubscriptionSuspended:
		return true
	default:
		return false
	}
}

// Errors reported by the subscription paths.
var (
	// ErrUnknownBillingCycle reports a billing cycle the machine cannot advance.
	ErrUnknownBillingCycle = errors.New("commerce: unknown billing cycle")
	// ErrSubscriptionNotFound reports a subscription that does not exist, or one
	// that belongs to someone else — the same answer for both, for the same
	// reason as orders.
	ErrSubscriptionNotFound = errors.New("commerce: no such subscription")
	// ErrSubscriptionNotLive reports an action against a subscription whose
	// terminal state has already been reached.
	ErrSubscriptionNotLive = errors.New("commerce: subscription is no longer live")
	// ErrNothingDue reports a renewal asked for while the current period has not
	// ended. The customer does not owe money yet, so there is no invoice to pay.
	ErrNothingDue = errors.New("commerce: the current period has not ended")
	// ErrInsufficientBalance reports a wallet that cannot back the renewal. The
	// schema's conditional UPDATE saw to it; this error is what the caller sees.
	ErrInsufficientBalance = errors.New("commerce: the balance does not cover the renewal")
	// ErrInvoiceNotFound reports an open renewal invoice where there is none. The
	// lost race that reads back the invoice after opening it can hit this only if
	// the winner's invoice was paid in between — which is the renewal done.
	ErrInvoiceNotFound = errors.New("commerce: no such open invoice")
)

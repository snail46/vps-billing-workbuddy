package commerce

// The state machines `docs/05` fixes, and the transitions between them.
//
// The document states the machines as words: an order goes pending→paid→fulfilling→
// fulfilled, or pending→cancelled, or paid→refund_pending→refunded. Putting them in a
// table rather than in a chain of conditionals is what lets a test compare the table
// against the document; a machine expressed as `if status == pending || status ==
// processing` in a handler cannot be compared against anything, and it is how a
// transition that the document never stated gets introduced by accident.
//
// The constants mirror the CHECK constraints in 0004_commerce. A value that is in one
// and not the other is a compile error or a constraint violation, never a state
// nothing handles.

// Order statuses.
const (
	OrderPending       = "pending"
	OrderPaid          = "paid"
	OrderFulfilling    = "fulfilling"
	OrderFulfilled     = "fulfilled"
	OrderCancelled     = "cancelled"
	OrderRefundPending = "refund_pending"
	OrderRefunded      = "refunded"
)

// Payment statuses.
const (
	PaymentPending           = "pending"
	PaymentProcessing        = "processing"
	PaymentSucceeded         = "succeeded"
	PaymentFailed            = "failed"
	PaymentPartiallyRefunded = "partially_refunded"
	PaymentRefunded          = "refunded"
)

// orderTransitions is `docs/05`'s order machine, arrow by arrow.
//
// Terminal states have no entry: an order that is fulfilled, cancelled or refunded
// does not move again, and `CanTransition` answering false for every target is how
// that is expressed rather than by a list of nothing.
var orderTransitions = map[string][]string{
	OrderPending: {
		OrderPaid,
		OrderCancelled,
	},
	OrderPaid: {
		OrderFulfilling,
		OrderRefundPending,
	},
	OrderFulfilling: {
		OrderFulfilled,
	},
	OrderRefundPending: {
		OrderRefunded,
	},
}

// paymentTransitions is the payment machine.
//
// `docs/05` gives the payment statuses as a set rather than as a chain, so the
// transitions here are the ones this phase needs, and they are stated rather than
// implied. The comment on each says which requirement it serves.
var paymentTransitions = map[string][]string{
	PaymentPending: {
		// The settlement path: a callback arrives for a payment that has not been
		// started with the gateway yet.
		PaymentSucceeded,
		PaymentProcessing,
		PaymentFailed,
	},
	PaymentProcessing: {
		PaymentSucceeded,
		PaymentFailed,
	},
	PaymentSucceeded: {
		// A refund is a later phase's flow; the edge is here because the document
		// lists a refunded payment, and leaving it out would mean the refund phase
		// has to edit this table rather than read it.
		PaymentPartiallyRefunded,
		PaymentRefunded,
	},
	PaymentPartiallyRefunded: {
		PaymentRefunded,
	},
}

// OrderStatuses returns every order status.
func OrderStatuses() []string {
	return []string{
		OrderPending, OrderPaid, OrderFulfilling, OrderFulfilled,
		OrderCancelled, OrderRefundPending, OrderRefunded,
	}
}

// PaymentStatuses returns every payment status.
func PaymentStatuses() []string {
	return []string{
		PaymentPending, PaymentProcessing, PaymentSucceeded,
		PaymentFailed, PaymentPartiallyRefunded, PaymentRefunded,
	}
}

// OrderCanTransition reports whether the order machine allows a move.
//
// It answers false for an unrecognised source as well as for an unrecognised target:
// a status the platform does not know is not a status it may move away from.
func OrderCanTransition(from, to string) bool {
	return contains(orderTransitions[from], to)
}

// PaymentCanTransition reports whether the payment machine allows a move.
func PaymentCanTransition(from, to string) bool {
	return contains(paymentTransitions[from], to)
}

// OrderIsTerminal reports whether an order has finished moving.
func OrderIsTerminal(status string) bool {
	return len(orderTransitions[status]) == 0 && contains(OrderStatuses(), status)
}

// PaymentIsSettled reports whether a payment has taken money.
//
// It is the predicate the ledger depends on: a payment in one of these states has a
// settlement recorded against it, so settling it again would be a second entry for
// the same money.
func PaymentIsSettled(status string) bool {
	switch status {
	case PaymentSucceeded, PaymentPartiallyRefunded, PaymentRefunded:
		return true
	default:
		return false
	}
}

// contains reports whether a slice holds a value.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

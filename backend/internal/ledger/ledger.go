// Package ledger holds the shape of the financial record and the rule that every
// posting balances.
//
// ADR-005 states the decision this package implements: the ledger is the source of
// truth for money, every transaction balances per currency, and the ledger is
// append-only — a mistake is corrected by an adjustment, never by an edit.
//
// Nothing here knows about PostgreSQL, wallets or orders. The statements live in an
// adapter and the projection lives in the domain that owns it, which is what keeps
// the rules testable without a database and — more importantly — what stops a rule
// from being re-decided wherever it is convenient.
package ledger

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// Account types.
//
// They mirror the CHECK constraint in 0004_commerce, so a new one is a migration
// rather than a typo that silently creates an account nothing reports on.
const (
	// AccountUserWallet is what the platform owes a customer. A credit increases it.
	AccountUserWallet = "user_wallet"
	// AccountRevenue is what the platform has earned. A credit increases it.
	AccountRevenue = "revenue"
	// AccountGatewayClearing is money received from a gateway but not yet settled to
	// the bank. An asset, and a debit increases it — which under this package's single
	// sign convention reads as a negative balance, and the convention is documented on
	// Entry.Signed.
	AccountGatewayClearing = "gateway_clearing"
)

// Transaction types.
const (
	// TypePaymentSettlement records a payment being received.
	TypePaymentSettlement = "payment_settlement"
	// TypeRefund records money returned to a customer.
	TypeRefund = "refund"
	// TypeAdjustment records a correction. It is the only way to fix a wrong entry,
	// because the entries themselves are never changed.
	TypeAdjustment = "adjustment"
)

// Directions. A magnitude plus a direction, never a signed amount: the reference
// declares this shape, and it is what makes an unbalanced transaction detectable,
// because the entries of one transaction are required to sum to zero.
const (
	DirectionDebit  = "debit"
	DirectionCredit = "credit"
)

// Platform account identifiers.
//
// `db/schema.sql` declares no accounts table, so `account_id` is an opaque reference
// and a platform-level account has no row to point at. A well-known constant is the
// honest way to name one; the alternative — inventing a user, or leaving the column
// null — would put a row into a table it does not belong in, or lose the ability to
// balance the transaction at all.
var (
	// AccountIDRevenue is the platform's income account.
	AccountIDRevenue = uuid.MustParse("00000000-0000-7000-8000-000000000001")
	// AccountIDGatewayClearing is the account holding money a gateway has taken but
	// not yet settled.
	AccountIDGatewayClearing = uuid.MustParse("00000000-0000-7000-8000-000000000002")
)

// Errors.
var (
	// ErrUnbalanced reports a set of entries whose debits and credits differ.
	ErrUnbalanced = errors.New("ledger: transaction does not balance")
	// ErrSingleSided reports a set of fewer than two entries.
	ErrSingleSided = errors.New("ledger: transaction needs at least two entries")
	// ErrUnknownAccountType reports an account type outside the closed set.
	ErrUnknownAccountType = errors.New("ledger: unknown account type")
	// ErrUnknownDirection reports a direction that is neither debit nor credit.
	ErrUnknownDirection = errors.New("ledger: unknown direction")
	// ErrUnknownType reports a transaction type outside the closed set.
	ErrUnknownType = errors.New("ledger: unknown transaction type")
	// ErrNoReference reports a transaction whose type requires a reference and has none.
	ErrNoReference = errors.New("ledger: transaction type requires a reference")
)

// knownAccountTypes is the closed set, kept beside the constants so adding one to
// either without the other is a visible omission rather than a silent divergence.
var knownAccountTypes = map[string]struct{}{
	AccountUserWallet:      {},
	AccountRevenue:         {},
	AccountGatewayClearing: {},
}

// knownTransactionTypes is the closed set of transaction types.
var knownTransactionTypes = map[string]struct{}{
	TypePaymentSettlement: {},
	TypeRefund:            {},
	TypeAdjustment:        {},
}

// Entry is one side of a transaction.
type Entry struct {
	AccountType string
	AccountID   uuid.UUID
	Direction   string
	// Amount is a magnitude. The direction carries the sign, so a negative amount is
	// not a way to express the opposite direction — Verify refuses it.
	Amount money.Money
}

// Signed returns the amount as it contributes to the account's balance.
//
// The convention is credit-positive for every account, applied uniformly so that a
// balance means the same thing wherever it is read. For a user wallet that is what an
// operator expects: credits are what the platform owes the customer, debits are what
// has been consumed. For an asset account it reads as the negative of the
// accountant's balance, which is a consequence of having one convention instead of
// two — and one convention is worth more than a sign that matches a textbook,
// because the alternative is a mapping that has to be remembered at each call site.
func (e Entry) Signed() int64 {
	if e.Direction == DirectionCredit {
		return e.Amount.AmountMinor
	}
	return -e.Amount.AmountMinor
}

// Transaction is a balanced set of entries with the fact it records.
type Transaction struct {
	ID   uuid.UUID
	Type string
	// ReferenceType and ReferenceID name the fact this transaction settles. A
	// reference is what makes "one settlement per payment" enforceable by the schema,
	// through the partial unique index on (type, reference_type, reference_id).
	ReferenceType string
	ReferenceID   uuid.UUID
	Description   string
	Entries       []Entry
}

// HasReference reports whether the transaction names a fact.
func (t Transaction) HasReference() bool {
	return t.ReferenceType != "" && t.ReferenceID != uuid.Nil
}

// Verify reports whether the transaction may be posted.
//
// It is a pure function of the transaction, so every rule about what a posting has to
// look like is checked in one place and can be exercised without a database. The
// store applies the same guarantees only insofar as its constraints can express them;
// this is where the rule lives.
func (t Transaction) Verify() error {
	if t.ID == uuid.Nil {
		return errors.New("ledger: transaction needs an identifier")
	}
	if _, ok := knownTransactionTypes[t.Type]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, t.Type)
	}
	// Adjustments are the one type that may stand alone: a correction is by nature
	// about something already recorded, and a future reconciler may post one with no
	// aggregate to point at. The settlement and refund types settle a specific fact,
	// and a settlement that names nothing cannot be checked for having happened twice
	// — which is the property the Gate is about.
	if t.Type != TypeAdjustment && !t.HasReference() {
		return fmt.Errorf("%w: %s", ErrNoReference, t.Type)
	}
	return Verify(t.Entries)
}

// Verify reports whether a set of entries is a balanced posting.
//
// The checks, and why each one is here:
//
//   - At least two entries. A single entry is a movement with no counterparty, so
//     "where did this money come from" has no answer in the data, and a double post
//     is indistinguishable from two legitimate ones.
//   - A known account type and a real identifier. A typo would otherwise create an
//     account that nothing reports on and nobody notices.
//   - A direction that is debit or credit, and an amount that is a magnitude. A
//     negative amount plus a direction is two ways to express the same sign, which is
//     one way too many.
//   - Debits equal credits **per currency**. Summing across currencies would let a
//     transaction balance by accident between two amounts that are not comparable,
//     and the platform's own multi-currency settlement would be the thing that
//     discovered it.
func Verify(entries []Entry) error {
	if len(entries) < 2 {
		return fmt.Errorf("%w: got %d", ErrSingleSided, len(entries))
	}

	// Debits and credits are accumulated per currency: a foreign exchange movement is
	// two balanced transactions, not one unbalanced one.
	type totals struct {
		debit  int64
		credit int64
	}
	byCurrency := make(map[money.Currency]*totals, 1)

	for i, entry := range entries {
		if _, ok := knownAccountTypes[entry.AccountType]; !ok {
			return fmt.Errorf("%w: entry %d has %q", ErrUnknownAccountType, i, entry.AccountType)
		}
		if entry.AccountID == uuid.Nil {
			return fmt.Errorf("ledger: entry %d has no account identifier", i)
		}
		if entry.Direction != DirectionDebit && entry.Direction != DirectionCredit {
			return fmt.Errorf("%w: entry %d has %q", ErrUnknownDirection, i, entry.Direction)
		}
		if entry.Amount.IsNegative() {
			return fmt.Errorf("ledger: entry %d has a negative amount (%d); the direction carries the sign",
				i, entry.Amount.AmountMinor)
		}
		if entry.Amount.Currency == "" {
			return fmt.Errorf("ledger: entry %d has no currency", i)
		}

		sum, ok := byCurrency[entry.Amount.Currency]
		if !ok {
			sum = &totals{}
			byCurrency[entry.Amount.Currency] = sum
		}
		if entry.Direction == DirectionDebit {
			sum.debit += entry.Amount.AmountMinor
		} else {
			sum.credit += entry.Amount.AmountMinor
		}
	}

	for currency, sum := range byCurrency {
		if sum.debit != sum.credit {
			return fmt.Errorf("%w: %s debits %d, credits %d",
				ErrUnbalanced, currency, sum.debit, sum.credit)
		}
	}
	return nil
}

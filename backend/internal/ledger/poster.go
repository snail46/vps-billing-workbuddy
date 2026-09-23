package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// Store persists transactions and reads balances back.
//
// It holds no SQL: the statements are an adapter's, which is what lets every rule
// about what a posting has to look like live in this package and be exercised without
// a database.
type Store interface {
	// PostTransaction writes a transaction and its entries.
	//
	// It is called only with a transaction that has passed Verify.
	PostTransaction(ctx context.Context, tx Transaction) error

	// SumAccount returns the credit-minus-debit total of one account in one currency.
	//
	// This is the derivation the projection is checked against, so it reads the
	// entries — never the projection itself.
	SumAccount(ctx context.Context, accountType string, accountID uuid.UUID, currency money.Currency) (int64, error)
}

// Projection is something maintained alongside the entries.
//
// The interface exists because the wallet balance is a projection of the ledger and
// the wallet table belongs to commerce, which imports this package — so the ledger
// cannot import it back. It is the dependency direction that makes the interface
// necessary rather than a guess at future flexibility.
//
// Apply must be called inside the same database transaction that wrote the entries.
// That is not enforced here, and it cannot be: what enforces it is that the Store and
// every Projection are built from the same transaction by the caller. A projection
// updated outside it is a balance that can disagree with the ledger after a rollback,
// which is the failure this whole arrangement exists to prevent.
type Projection interface {
	Apply(ctx context.Context, entries []Entry) error
}

// Poster writes transactions and keeps the projections of them up to date.
type Poster struct {
	store       Store
	projections []Projection
}

// NewPoster builds a poster over a store, bound to whatever transaction the store is
// bound to.
func NewPoster(store Store, projections ...Projection) *Poster {
	return &Poster{store: store, projections: projections}
}

// Post verifies a transaction, writes it, and applies every projection to it.
//
// The order is deliberate and is the whole point of the method existing: the entries
// are validated before anything is written, so a posting that does not balance leaves
// no trace at all rather than leaving a transaction row whose entries are missing.
func (p *Poster) Post(ctx context.Context, tx Transaction) error {
	if err := tx.Verify(); err != nil {
		return err
	}

	if err := p.store.PostTransaction(ctx, tx); err != nil {
		return fmt.Errorf("ledger: write transaction: %w", err)
	}

	for _, projection := range p.projections {
		if err := projection.Apply(ctx, tx.Entries); err != nil {
			return fmt.Errorf("ledger: apply projection: %w", err)
		}
	}
	return nil
}

// Balance returns an account's balance as derived from the entries.
//
// It reads the ledger rather than any projection of it, which is what makes it the
// answer to "is the projection right". Nothing in Phase 2 calls it in production —
// no endpoint displays a balance yet — but it is the operation the Gate's own
// assertion uses, and the reconciler in Phase 11 is what will call it in anger.
// A projection that cannot be checked against its source is a second opinion, not a
// record.
func (p *Poster) Balance(ctx context.Context, accountType string, accountID uuid.UUID, currency money.Currency) (money.Money, error) {
	total, err := p.store.SumAccount(ctx, accountType, accountID, currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("ledger: read balance: %w", err)
	}
	return money.Money{AmountMinor: total, Currency: currency}, nil
}

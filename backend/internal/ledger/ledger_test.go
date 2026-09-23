package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/ledger"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// These tests cover the rules a posting has to satisfy. They need no database, which
// is the point of the rules living in this package rather than in the adapter: the
// first place a wrong posting can be caught is before anything is written.

func amount(t *testing.T, minor int64, currency string) money.Money {
	t.Helper()
	value, err := money.New(minor, currency)
	if err != nil {
		t.Fatalf("build amount: %v", err)
	}
	return value
}

func walletEntry(t *testing.T, direction string, minor int64) ledger.Entry {
	t.Helper()
	return ledger.Entry{
		AccountType: ledger.AccountUserWallet,
		AccountID:   uuid.MustParse("0198f1c2-0000-7000-8000-0000000000c1"),
		Direction:   direction,
		Amount:      amount(t, minor, "CNY"),
	}
}

func platformEntry(direction string, minor int64) ledger.Entry {
	return ledger.Entry{
		AccountType: ledger.AccountRevenue,
		AccountID:   ledger.AccountIDRevenue,
		Direction:   direction,
		Amount:      money.Money{AmountMinor: minor, Currency: "CNY"},
	}
}

func TestVerifyAcceptsABalancedSettlement(t *testing.T) {
	// Money arriving from a gateway: the clearing asset increases by a debit, and the
	// platform's income increases by the matching credit.
	entries := []ledger.Entry{
		{
			AccountType: ledger.AccountGatewayClearing,
			AccountID:   ledger.AccountIDGatewayClearing,
			Direction:   ledger.DirectionDebit,
			Amount:      money.Money{AmountMinor: 9900, Currency: "CNY"},
		},
		platformEntry(ledger.DirectionCredit, 9900),
	}
	if err := ledger.Verify(entries); err != nil {
		t.Fatalf("a balanced settlement was refused: %v", err)
	}
}

func TestVerifyRefusesASingleSidedEntry(t *testing.T) {
	// One entry is a movement with no counterparty: "where did this come from" has no
	// answer in the data, and a double post becomes indistinguishable from two
	// legitimate ones.
	for _, entries := range [][]ledger.Entry{
		{platformEntry(ledger.DirectionCredit, 500)},
		nil,
	} {
		err := ledger.Verify(entries)
		if !errors.Is(err, ledger.ErrSingleSided) {
			t.Errorf("expected ErrSingleSided, got %v", err)
		}
	}
}

func TestVerifyRefusesAnUnbalancedSet(t *testing.T) {
	entries := []ledger.Entry{
		platformEntry(ledger.DirectionDebit, 1000),
		platformEntry(ledger.DirectionCredit, 999),
	}

	err := ledger.Verify(entries)
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
	// The message has to name the amounts: it is the only thing an operator has to
	// work from when a posting is refused in production.
	if message := err.Error(); !strings.Contains(message, "1000") || !strings.Contains(message, "999") {
		t.Errorf("the refusal does not say what was wrong: %q", message)
	}
}

func TestVerifyBalancesPerCurrency(t *testing.T) {
	cny := func(direction string, minor int64) ledger.Entry {
		return ledger.Entry{
			AccountType: ledger.AccountRevenue, AccountID: ledger.AccountIDRevenue,
			Direction: direction, Amount: money.Money{AmountMinor: minor, Currency: "CNY"},
		}
	}
	usd := func(direction string, minor int64) ledger.Entry {
		return ledger.Entry{
			AccountType: ledger.AccountRevenue, AccountID: ledger.AccountIDRevenue,
			Direction: direction, Amount: money.Money{AmountMinor: minor, Currency: "USD"},
		}
	}

	// Both currencies balance, so the set is valid: a movement in two currencies is
	// two balanced parts, not one unbalanced one.
	if err := ledger.Verify([]ledger.Entry{
		cny(ledger.DirectionDebit, 100), cny(ledger.DirectionCredit, 100),
		usd(ledger.DirectionDebit, 50), usd(ledger.DirectionCredit, 50),
	}); err != nil {
		t.Fatalf("two balanced currencies were refused: %v", err)
	}

	// Balances in aggregate but not per currency. Summing across currencies would
	// accept this, and the platform's first multi-currency settlement would be where
	// it was discovered.
	err := ledger.Verify([]ledger.Entry{
		cny(ledger.DirectionDebit, 100), cny(ledger.DirectionCredit, 60),
		usd(ledger.DirectionDebit, 10), usd(ledger.DirectionCredit, 50),
	})
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("a set that balances only in aggregate was accepted: %v", err)
	}
}

func TestVerifyRefusesAMalformedEntry(t *testing.T) {
	cases := map[string]ledger.Entry{
		"unknown account type": {
			AccountType: "pocket", AccountID: uuid.New(),
			Direction: ledger.DirectionDebit, Amount: money.Money{AmountMinor: 1, Currency: "CNY"},
		},
		"no account identifier": {
			AccountType: ledger.AccountRevenue, AccountID: uuid.Nil,
			Direction: ledger.DirectionDebit, Amount: money.Money{AmountMinor: 1, Currency: "CNY"},
		},
		"unknown direction": {
			AccountType: ledger.AccountRevenue, AccountID: ledger.AccountIDRevenue,
			Direction: "sideways", Amount: money.Money{AmountMinor: 1, Currency: "CNY"},
		},
		"negative amount": {
			AccountType: ledger.AccountRevenue, AccountID: ledger.AccountIDRevenue,
			Direction: ledger.DirectionCredit, Amount: money.Money{AmountMinor: -1, Currency: "CNY"},
		},
		"no currency": {
			AccountType: ledger.AccountRevenue, AccountID: ledger.AccountIDRevenue,
			Direction: ledger.DirectionCredit, Amount: money.Money{AmountMinor: 1},
		},
	}

	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			// Paired with a legitimate opposite entry, so the refusal is about the
			// malformed entry rather than about the set being single-sided.
			other := platformEntry(ledger.DirectionCredit, 1)
			if entry.Direction == ledger.DirectionCredit {
				other = platformEntry(ledger.DirectionDebit, 1)
			}
			if err := ledger.Verify([]ledger.Entry{entry, other}); err == nil {
				t.Errorf("a %s was accepted", name)
			}
		})
	}
}

func TestEntrySignedIsCreditPositiveForEveryAccount(t *testing.T) {
	// One convention, applied uniformly, so a balance means the same thing wherever it
	// is read. For a wallet that is what an operator expects; for an asset account it
	// is the negative of the textbook balance, which is the price of not having two
	// conventions and a mapping that has to be remembered at each call site.
	credit := walletEntry(t, ledger.DirectionCredit, 4200)
	debit := walletEntry(t, ledger.DirectionDebit, 4200)

	if credit.Signed() != 4200 {
		t.Errorf("a credit contributed %d", credit.Signed())
	}
	if debit.Signed() != -4200 {
		t.Errorf("a debit contributed %d", debit.Signed())
	}
}

func TestTransactionVerifyRequiresAReferenceWhereItMatters(t *testing.T) {
	entries := []ledger.Entry{
		platformEntry(ledger.DirectionDebit, 100),
		platformEntry(ledger.DirectionCredit, 100),
	}

	settlement := ledger.Transaction{
		ID: uuid.New(), Type: ledger.TypePaymentSettlement, Entries: entries,
	}
	// A settlement that names nothing cannot be checked for having happened twice,
	// which is exactly the property the Gate is about.
	if err := settlement.Verify(); !errors.Is(err, ledger.ErrNoReference) {
		t.Errorf("a settlement with no reference was accepted: %v", err)
	}

	settlement.ReferenceType = "payment"
	settlement.ReferenceID = uuid.New()
	if err := settlement.Verify(); err != nil {
		t.Errorf("a referenced settlement was refused: %v", err)
	}

	// An adjustment is the exception: a correction is by nature about something
	// already recorded, and a future reconciler may post one with no aggregate to
	// point at.
	adjustment := ledger.Transaction{ID: uuid.New(), Type: ledger.TypeAdjustment, Entries: entries}
	if err := adjustment.Verify(); err != nil {
		t.Errorf("an adjustment with no reference was refused: %v", err)
	}
}

func TestTransactionVerifyRefusesAnUnknownTypeOrNoIdentifier(t *testing.T) {
	entries := []ledger.Entry{
		platformEntry(ledger.DirectionDebit, 100),
		platformEntry(ledger.DirectionCredit, 100),
	}

	if err := (ledger.Transaction{ID: uuid.New(), Type: "gift", Entries: entries}).Verify(); !errors.Is(err, ledger.ErrUnknownType) {
		t.Errorf("an unknown type was accepted: %v", err)
	}
	if err := (ledger.Transaction{Type: ledger.TypeAdjustment, Entries: entries}).Verify(); err == nil {
		t.Error("a transaction with no identifier was accepted")
	}
}

// storeFake records what a poster asked it to write.
type storeFake struct {
	written  []ledger.Transaction
	sums     map[string]int64
	failWith error
}

func newStoreFake() *storeFake {
	return &storeFake{sums: map[string]int64{}}
}

func (f *storeFake) PostTransaction(_ context.Context, tx ledger.Transaction) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.written = append(f.written, tx)
	return nil
}

func (f *storeFake) SumAccount(_ context.Context, accountType string, accountID uuid.UUID, currency money.Currency) (int64, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	return f.sums[accountType+":"+accountID.String()+":"+string(currency)], nil
}

// projectionFake records the entries it was handed.
type projectionFake struct {
	applied  [][]ledger.Entry
	failWith error
}

func (f *projectionFake) Apply(_ context.Context, entries []ledger.Entry) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.applied = append(f.applied, entries)
	return nil
}

func TestPosterRefusesAnInvalidTransactionWithoutWriting(t *testing.T) {
	store := newStoreFake()
	projection := &projectionFake{}
	poster := ledger.NewPoster(store, projection)

	// Unbalanced. The validation happens before the write, which is what makes a
	// refused posting leave no trace at all rather than leaving a transaction row
	// whose entries are missing.
	err := poster.Post(context.Background(), ledger.Transaction{
		ID: uuid.New(), Type: ledger.TypeAdjustment,
		Entries: []ledger.Entry{
			platformEntry(ledger.DirectionDebit, 100),
			platformEntry(ledger.DirectionCredit, 99),
		},
	})
	if err == nil {
		t.Fatal("an unbalanced transaction was posted")
	}
	if len(store.written) != 0 {
		t.Errorf("a refused transaction was written: %+v", store.written)
	}
	if len(projection.applied) != 0 {
		t.Errorf("a refused transaction reached a projection: %+v", projection.applied)
	}
}

func TestPosterWritesThenAppliesEveryProjection(t *testing.T) {
	store := newStoreFake()
	first := &projectionFake{}
	second := &projectionFake{}
	poster := ledger.NewPoster(store, first, second)

	tx := ledger.Transaction{
		ID: uuid.New(), Type: ledger.TypeAdjustment,
		Entries: []ledger.Entry{
			platformEntry(ledger.DirectionDebit, 700),
			platformEntry(ledger.DirectionCredit, 700),
		},
	}
	if err := poster.Post(context.Background(), tx); err != nil {
		t.Fatalf("post: %v", err)
	}

	if len(store.written) != 1 {
		t.Fatalf("the store received %d transactions", len(store.written))
	}
	// Both projections see the same entries: the wallet balance is one of them, and a
	// future projection (a revenue report, say) is added without touching this code.
	if len(first.applied) != 1 || len(second.applied) != 1 {
		t.Fatalf("projections applied: %d and %d", len(first.applied), len(second.applied))
	}
	if len(first.applied[0]) != 2 {
		t.Errorf("the projection received %d entries", len(first.applied[0]))
	}
}

func TestPosterPropagatesAFailedProjection(t *testing.T) {
	store := newStoreFake()
	broken := &projectionFake{failWith: errors.New("wallet row is missing")}
	poster := ledger.NewPoster(store, broken)

	err := poster.Post(context.Background(), ledger.Transaction{
		ID: uuid.New(), Type: ledger.TypeAdjustment,
		Entries: []ledger.Entry{
			platformEntry(ledger.DirectionDebit, 10),
			platformEntry(ledger.DirectionCredit, 10),
		},
	})

	// It has to surface. The caller is inside a database transaction: a projection
	// that failed silently would commit the entries and leave the projection behind
	// them, which is a balance that disagrees with the ledger and no record of why.
	if err == nil {
		t.Fatal("a failed projection was swallowed")
	}
	if !strings.Contains(err.Error(), "wallet row is missing") {
		t.Errorf("the cause was lost: %v", err)
	}
}

func TestPosterBalanceReadsTheEntriesNotTheProjection(t *testing.T) {
	store := newStoreFake()
	accountID := uuid.MustParse("0198f1c2-0000-7000-8000-0000000000c1")
	store.sums[ledger.AccountUserWallet+":"+accountID.String()+":CNY"] = 12345

	poster := ledger.NewPoster(store)
	balance, err := poster.Balance(context.Background(), ledger.AccountUserWallet, accountID, "CNY")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.AmountMinor != 12345 || balance.Currency != "CNY" {
		t.Errorf("balance = %+v", balance)
	}
}

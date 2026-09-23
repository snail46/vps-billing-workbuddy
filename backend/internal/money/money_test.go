package money_test

import (
	"errors"
	"testing"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

func TestNewCurrencyAcceptsOnlyAThreeLetterCode(t *testing.T) {
	for _, code := range []string{"CNY", "USD", "EUR", "JPY"} {
		if _, err := money.NewCurrency(code); err != nil {
			t.Errorf("NewCurrency(%q) returned %v", code, err)
		}
	}

	// Lower case is refused rather than upper-cased. The column's CHECK constraint
	// states the same shape, so accepting "cny" here would produce an amount that the
	// store rejects — better to fail where the mistake was made.
	for _, code := range []string{"cny", "CnY", "CN", "CNYY", "C1Y", "", " C NY"} {
		if _, err := money.NewCurrency(code); !errors.Is(err, money.ErrCurrencyUnsupported) {
			t.Errorf("NewCurrency(%q) returned %v, expected a refusal", code, err)
		}
	}
}

func TestArithmeticRefusesToMixCurrencies(t *testing.T) {
	cny := money.Money{AmountMinor: 1000, Currency: "CNY"}
	usd := money.Money{AmountMinor: 1000, Currency: "USD"}

	// Two amounts in different currencies are not comparable, and adding them is the
	// mistake that a bare int64 makes easy. The currency being part of the value is
	// what makes this a returned error rather than a wrong number.
	if _, err := cny.Add(usd); err == nil {
		t.Error("adding USD to CNY was allowed")
	}
	if _, err := cny.Sub(usd); err == nil {
		t.Error("subtracting USD from CNY was allowed")
	}

	sum, err := cny.Add(money.Money{AmountMinor: 500, Currency: "CNY"})
	if err != nil {
		t.Fatalf("adding like currencies: %v", err)
	}
	if sum.AmountMinor != 1500 {
		t.Errorf("sum = %d", sum.AmountMinor)
	}

	difference, err := cny.Sub(money.Money{AmountMinor: 1500, Currency: "CNY"})
	if err != nil {
		t.Fatalf("subtracting like currencies: %v", err)
	}
	// A balance can be negative, which is why the type allows it: refusing to
	// represent it would mean the value gets represented some other way.
	if difference.AmountMinor != -500 || !difference.IsNegative() {
		t.Errorf("difference = %d", difference.AmountMinor)
	}
}

func TestTimesRefusesANegativeQuantity(t *testing.T) {
	price := money.Money{AmountMinor: 1999, Currency: "CNY"}

	total, err := price.Times(3)
	if err != nil {
		t.Fatalf("Times(3): %v", err)
	}
	if total.AmountMinor != 5997 {
		t.Errorf("total = %d, expected 5997", total.AmountMinor)
	}

	// A negative quantity is a refund expressed as a line item, which would hide a
	// reversal inside an order instead of recording it as one.
	if _, err := price.Times(-1); err == nil {
		t.Error("multiplying by a negative quantity was allowed")
	}
}

func TestNegateAndZero(t *testing.T) {
	amount := money.Money{AmountMinor: 250, Currency: "USD"}
	if got := amount.Negate().AmountMinor; got != -250 {
		t.Errorf("Negate = %d", got)
	}
	if !money.Zero("USD").IsZero() {
		t.Error("Zero is not zero")
	}
	if money.Zero("USD").Currency != "USD" {
		t.Error("Zero lost its currency")
	}
}

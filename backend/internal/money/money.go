// Package money holds monetary amounts in minor units.
//
// Every amount in this platform is an integer count of a currency's minor unit —
// cents, fen, or for a currency without a fractional part, whole units — carried as
// an int64 with the currency beside it. No floating point value appears anywhere in
// the path from a price to a ledger entry, because a binary fraction cannot represent
// 0.1 and the error that introduces is invisible until it is a reconciliation that
// does not balance.
//
// The currency is part of the value rather than a parameter of every operation.
// Adding two amounts in different currencies is then a compile-time impossibility
// rather than a check somebody has to remember, which is the same reason the ledger's
// balance check is expressed per currency.
package money

import (
	"errors"
	"fmt"
	"strings"
)

// ErrCurrencyUnsupported reports a currency code that is not a three-letter
// upper-case code.
//
// The check is on the shape and not on a list of currencies, because the platform
// sells in whatever its operators configure: a closed list here would be a list that
// has to be edited before a new market can be opened, and the column's own CHECK
// constraint states the same shape.
var ErrCurrencyUnsupported = errors.New("money: currency is not a three-letter code")

// Currency is an ISO 4217 code, upper case.
type Currency string

// NewCurrency validates and returns a currency code.
func NewCurrency(code string) (Currency, error) {
	if len(code) != 3 {
		return "", fmt.Errorf("%w: %q", ErrCurrencyUnsupported, code)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("%w: %q", ErrCurrencyUnsupported, code)
		}
	}
	return Currency(code), nil
}

// String implements fmt.Stringer.
func (c Currency) String() string { return string(c) }

// Money is an amount in the minor unit of its currency.
type Money struct {
	// AmountMinor may be negative. A price is never negative, and a ledger entry
	// never is either — its direction carries the sign — but a balance and a
	// correction both can be, and a type that cannot express the value it is used for
	// is a type that gets worked around.
	AmountMinor int64
	Currency    Currency
}

// New returns an amount, validating the currency.
func New(amountMinor int64, currency string) (Money, error) {
	code, err := NewCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: amountMinor, Currency: code}, nil
}

// Zero returns a zero amount in a currency.
func Zero(currency Currency) Money { return Money{Currency: currency} }

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool { return m.AmountMinor == 0 }

// IsNegative reports whether the amount is below zero.
func (m Money) IsNegative() bool { return m.AmountMinor < 0 }

// Add returns the sum of two amounts in the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("money: cannot add %s to %s", other.Currency, m.Currency)
	}
	return Money{AmountMinor: m.AmountMinor + other.AmountMinor, Currency: m.Currency}, nil
}

// Sub returns the difference of two amounts in the same currency.
func (m Money) Sub(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("money: cannot subtract %s from %s", other.Currency, m.Currency)
	}
	return Money{AmountMinor: m.AmountMinor - other.AmountMinor, Currency: m.Currency}, nil
}

// Negate returns the amount with its sign reversed.
func (m Money) Negate() Money { return Money{AmountMinor: -m.AmountMinor, Currency: m.Currency} }

// Times returns the amount multiplied by a whole number of units.
//
// Multiplication by an integer cannot introduce a fractional part, so the result is
// exact. A percentage or a pro-rated discount would not be, which is why neither is
// offered here: the caller has to state how it rounds, and that statement belongs
// where it can be read next to the rule that required it.
func (m Money) Times(quantity int64) (Money, error) {
	if quantity < 0 {
		return Money{}, fmt.Errorf("money: cannot multiply by a negative quantity (%d)", quantity)
	}
	return Money{AmountMinor: m.AmountMinor * quantity, Currency: m.Currency}, nil
}

// String renders the amount for a log line or an error message.
//
// It is deliberately not a presentation format: the exponent of a currency decides
// how many minor units make one unit, and that belongs to the client, which knows the
// viewer's locale (docs/13). Writing "1234" as "12.34" here would be a guess.
func (m Money) String() string {
	return fmt.Sprintf("%d %s (minor)", m.AmountMinor, strings.ToUpper(string(m.Currency)))
}

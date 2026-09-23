package commerce_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
)

// The subscription machine is held the same way the order machine is: the
// vocabulary comes from docs/05, and the machine is not permitted to use a word
// the document does not have. The edges themselves are this repository's decision
// (ADR-006 — the document fixes states, not transitions), so the edges are
// asserted against the decision's own consequences: no invented state, no exit
// from a terminal one, and `pending` unused until a phase needs it.

func TestSubscriptionStatusesMatchTheDocument(t *testing.T) {
	doc := statusesFromDoc(t, "Subscription")
	known := map[string]bool{}
	for _, status := range commerce.SubscriptionStatuses() {
		known[status] = true
	}

	// Both directions. A status the document lists and the machine lacks is a
	// vocabulary the code has already abandoned; one the machine holds and the
	// document lacks is a state nothing external can describe.
	for _, status := range doc {
		if !known[status] {
			t.Errorf("docs/05 lists subscription status %q, which the machine does not know", status)
		}
	}
	for status := range known {
		if !containsString(doc, status) {
			t.Errorf("the machine knows subscription status %q, which docs/05 does not list", status)
		}
	}
}

func TestSubscriptionEdgesStayInsideTheVocabulary(t *testing.T) {
	doc := statusesFromDoc(t, "Subscription")
	known := map[string]bool{}
	for _, status := range doc {
		known[status] = true
	}

	// Every edge's target must be a word the document has. The edges are
	// ADR-006's; inventing a state while drawing one is how a status nothing
	// external can name gets into the table.
	for _, status := range commerce.SubscriptionStatuses() {
		for _, next := range []string{
			commerce.SubscriptionActive, commerce.SubscriptionPastDue,
			commerce.SubscriptionSuspended, commerce.SubscriptionCancelled,
			commerce.SubscriptionExpired, commerce.SubscriptionTerminated,
		} {
			if !commerce.SubscriptionCanTransition(status, next) {
				continue
			}
			if !known[next] {
				t.Errorf("the machine moves %s -> %s, but %q is not in docs/05's vocabulary",
					status, next, next)
			}
		}
	}
}

func TestSubscriptionTerminalStatesDoNotMove(t *testing.T) {
	for _, terminal := range []string{
		commerce.SubscriptionCancelled,
		commerce.SubscriptionExpired,
		commerce.SubscriptionTerminated,
	} {
		for _, next := range commerce.SubscriptionStatuses() {
			if commerce.SubscriptionCanTransition(terminal, next) {
				t.Errorf("terminal state %s moves to %s; a terminal state has no exit", terminal, next)
			}
		}
	}
}

func TestSubscriptionIsLiveMatchesTheMachine(t *testing.T) {
	live := map[string]bool{
		commerce.SubscriptionActive:    true,
		commerce.SubscriptionPastDue:   true,
		commerce.SubscriptionSuspended: true,
	}
	for _, status := range commerce.SubscriptionStatuses() {
		if commerce.SubscriptionIsLive(status) != live[status] {
			t.Errorf("SubscriptionIsLive(%q) disagrees with the machine", status)
		}
	}
}

func TestAddBillingCycleAdvancesCalendarMonths(t *testing.T) {
	cases := []struct {
		cycle string
		from  string
		want  string
	}{
		// The month-end case is the reason the arithmetic is calendar-aware: a
		// subscription that starts on the 31st renews on the 28th of February
		// rather than drifting past a month of service.
		{"monthly", "2026-01-31T12:00:00Z", "2026-02-28T12:00:00Z"},
		{"monthly", "2026-02-28T12:00:00Z", "2026-03-28T12:00:00Z"},
		{"quarterly", "2026-01-15T00:00:00Z", "2026-04-15T00:00:00Z"},
		{"semiannually", "2026-01-15T00:00:00Z", "2026-07-15T00:00:00Z"},
		{"annually", "2024-02-29T00:00:00Z", "2025-02-28T00:00:00Z"},
	}
	for _, c := range cases {
		from, err := time.Parse(time.RFC3339, c.from)
		if err != nil {
			t.Fatalf("parse %s: %v", c.from, err)
		}
		want, err := time.Parse(time.RFC3339, c.want)
		if err != nil {
			t.Fatalf("parse %s: %v", c.want, err)
		}
		got, err := commerce.AddBillingCycle(c.cycle, from)
		if err != nil {
			t.Fatalf("%s from %s: %v", c.cycle, c.from, err)
		}
		if !got.Equal(want) {
			t.Errorf("%s from %s = %s, expected %s", c.cycle, c.from, got.Format(time.RFC3339), c.want)
		}
	}
}

func TestAddBillingCycleRefusesAnUnknownCycle(t *testing.T) {
	if _, err := commerce.AddBillingCycle("weekly", time.Now()); !errors.Is(err, commerce.ErrUnknownBillingCycle) {
		t.Fatalf("an unknown cycle produced %v", err)
	}
	// The message names the value: it is what an operator has to work from when
	// a plan row carries something the machine does not know.
	if _, err := commerce.AddBillingCycle("weekly", time.Now()); !strings.Contains(err.Error(), "weekly") {
		t.Errorf("the refusal does not name the cycle: %v", err)
	}
}

func TestSubscriptionDeadlineFollowsTheState(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if got := commerce.SubscriptionDeadline(commerce.SubscriptionPastDue, at); !got.Equal(at.Add(72 * time.Hour)) {
		t.Errorf("past due resolves at %v, expected %v", got, at.Add(72*time.Hour))
	}
	if got := commerce.SubscriptionDeadline(commerce.SubscriptionSuspended, at); !got.Equal(at.Add(14 * 24 * time.Hour)) {
		t.Errorf("suspended resolves at %v, expected %v", got, at.Add(14*24*time.Hour))
	}
	// A state with no deadline — active — resolves never, which the zero time
	// expresses rather than a far-future date that pretends to be a policy.
	if got := commerce.SubscriptionDeadline(commerce.SubscriptionActive, at); !got.IsZero() {
		t.Errorf("active resolved at %v; it has no deadline", got)
	}
}

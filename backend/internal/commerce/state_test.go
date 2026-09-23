package commerce_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
)

// The machines are compared against `docs/05` rather than against a copy of it in
// this file. A copy would agree with the code and disagree with the document, which is
// the one arrangement that hides the disagreement; reading the document means editing
// the machine without editing the document is a failing test.
//
// This is the same technique the migration guard uses: derive the expectation from the
// artefact that is authoritative, rather than restating it.

const stateMachineDoc = "../../../docs/05-STATE-MACHINES.md"

// docLine returns the body of the `Key:` line in the state machine document.
func docLine(t *testing.T, key string) string {
	t.Helper()

	contents, err := os.ReadFile(filepath.FromSlash(stateMachineDoc))
	if err != nil {
		t.Fatalf("read %s: %v", stateMachineDoc, err)
	}

	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.*)$`)
	match := pattern.FindStringSubmatch(string(contents))
	if match == nil {
		t.Fatalf("no %s line in %s; the document was restructured", key, stateMachineDoc)
	}
	return strings.TrimSpace(match[1])
}

// chains splits a line into its arrow chains.
//
// The document separates alternative chains with a full-width semicolon. Splitting on
// both forms means a future edit that uses the ASCII one does not silently produce a
// single chain containing a semicolon.
func chains(line string) [][]string {
	var out [][]string
	for _, part := range strings.FieldsFunc(line, func(r rune) bool { return r == '；' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		steps := strings.Split(part, "→")
		for i := range steps {
			steps[i] = strings.TrimSpace(steps[i])
		}
		out = append(out, steps)
	}
	return out
}

// transitionsFromDoc reads the order machine out of the document as pairs.
func transitionsFromDoc(t *testing.T) []string {
	t.Helper()

	var pairs []string
	for _, chain := range chains(docLine(t, "Order")) {
		for i := 0; i+1 < len(chain); i++ {
			pairs = append(pairs, chain[i]+"->"+chain[i+1])
		}
	}
	if len(pairs) == 0 {
		t.Fatal("the document yielded no order transitions; the parse is wrong")
	}
	sort.Strings(pairs)
	return pairs
}

// statusesFromDoc reads a slash-separated status list.
func statusesFromDoc(t *testing.T, key string) []string {
	t.Helper()

	var statuses []string
	for _, status := range strings.Split(docLine(t, key), "/") {
		if status = strings.TrimSpace(status); status != "" {
			statuses = append(statuses, status)
		}
	}
	if len(statuses) == 0 {
		t.Fatalf("the document yielded no statuses for %s", key)
	}
	sort.Strings(statuses)
	return statuses
}

// everyOrderTransition asks the machine about every ordered pair of statuses, so that
// an edge the document does not state is found rather than the other way round.
func everyOrderTransition() []string {
	statuses := commerce.OrderStatuses()
	var allowed []string
	for _, from := range statuses {
		for _, to := range statuses {
			if commerce.OrderCanTransition(from, to) {
				allowed = append(allowed, from+"->"+to)
			}
		}
	}
	sort.Strings(allowed)
	return allowed
}

func TestOrderMachineMatchesTheDocument(t *testing.T) {
	want := transitionsFromDoc(t)
	got := everyOrderTransition()

	// Both directions are asserted. Missing an edge means the document says something
	// the platform cannot do; an extra edge means the platform does something the
	// document never stated, which is how a state nothing handles gets invented.
	for _, pair := range want {
		if !containsString(got, pair) {
			t.Errorf("docs/05 states %s but the machine refuses it", pair)
		}
	}
	for _, pair := range got {
		if !containsString(want, pair) {
			t.Errorf("the machine allows %s, which docs/05 does not state", pair)
		}
	}
}

func TestOrderStatusesMatchTheDocument(t *testing.T) {
	var documented []string
	seen := map[string]struct{}{}
	for _, chain := range chains(docLine(t, "Order")) {
		for _, status := range chain {
			if _, ok := seen[status]; !ok {
				seen[status] = struct{}{}
				documented = append(documented, status)
			}
		}
	}
	sort.Strings(documented)

	known := commerce.OrderStatuses()
	sort.Strings(known)

	if strings.Join(documented, ",") != strings.Join(known, ",") {
		t.Errorf("the machines disagree:\ndocs/05: %v\ncode:    %v", documented, known)
	}
}

func TestPaymentStatusesMatchTheDocument(t *testing.T) {
	want := statusesFromDoc(t, "Payment")

	got := commerce.PaymentStatuses()
	sort.Strings(got)

	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("the payment statuses disagree:\ndocs/05: %v\ncode:    %v", want, got)
	}
}

func TestOrderStatusesAreUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for _, status := range commerce.OrderStatuses() {
		if _, ok := seen[status]; ok {
			t.Errorf("%q appears twice", status)
		}
		seen[status] = struct{}{}
	}
}

func TestTerminalOrderStatusesHaveNoTransitions(t *testing.T) {
	// docs/05 gives no edge out of these three. `CanTransition` answering false for
	// every target is how a terminal state is expressed.
	for _, status := range []string{commerce.OrderFulfilled, commerce.OrderCancelled, commerce.OrderRefunded} {
		if !commerce.OrderIsTerminal(status) {
			t.Errorf("%s is not reported as terminal", status)
		}
		for _, target := range commerce.OrderStatuses() {
			if commerce.OrderCanTransition(status, target) {
				t.Errorf("%s can move to %s", status, target)
			}
		}
	}

	for _, status := range []string{commerce.OrderPending, commerce.OrderPaid, commerce.OrderFulfilling, commerce.OrderRefundPending} {
		if commerce.OrderIsTerminal(status) {
			t.Errorf("%s is reported as terminal", status)
		}
	}
}

func TestUnknownStatusesCannotTransition(t *testing.T) {
	// A status the platform does not know is not a status it may move away from.
	for _, unknown := range []string{"", "PAID", "unknown", "pending "} {
		if commerce.OrderCanTransition(unknown, commerce.OrderPaid) {
			t.Errorf("an unrecognised order status %q could transition", unknown)
		}
		if commerce.PaymentCanTransition(unknown, commerce.PaymentSucceeded) {
			t.Errorf("an unrecognised payment status %q could transition", unknown)
		}
		if commerce.OrderIsTerminal(unknown) {
			t.Errorf("an unrecognised order status %q was reported as terminal", unknown)
		}
	}
}

func TestPaymentIsSettledCoversExactlyTheStatesThatTookMoney(t *testing.T) {
	// The predicate the ledger depends on. A payment in one of these has a settlement
	// recorded against it, so settling it again would be a second entry for the same
	// money — which is what the Gate is about.
	for _, status := range []string{
		commerce.PaymentSucceeded, commerce.PaymentPartiallyRefunded, commerce.PaymentRefunded,
	} {
		if !commerce.PaymentIsSettled(status) {
			t.Errorf("%s is not reported as settled", status)
		}
	}
	for _, status := range []string{
		commerce.PaymentPending, commerce.PaymentProcessing, commerce.PaymentFailed,
	} {
		if commerce.PaymentIsSettled(status) {
			t.Errorf("%s is reported as settled", status)
		}
	}
}

func TestSettlementIsTheOnlyPathToASucceededPayment(t *testing.T) {
	// The conditional transition the settlement service performs is
	// pending|processing → succeeded. Asserting that both are allowed here means the
	// service's own query cannot be written against a machine that forbids it.
	if !commerce.PaymentCanTransition(commerce.PaymentPending, commerce.PaymentSucceeded) {
		t.Error("a pending payment cannot be settled")
	}
	if !commerce.PaymentCanTransition(commerce.PaymentProcessing, commerce.PaymentSucceeded) {
		t.Error("a processing payment cannot be settled")
	}
	// And a settled payment cannot be settled again. This is the machine's half of the
	// idempotency guarantee; the schema's half is the partial unique index on the
	// ledger transaction's reference.
	for _, status := range []string{
		commerce.PaymentSucceeded, commerce.PaymentPartiallyRefunded, commerce.PaymentRefunded, commerce.PaymentFailed,
	} {
		if commerce.PaymentCanTransition(status, commerce.PaymentSucceeded) {
			t.Errorf("%s can transition to succeeded", status)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

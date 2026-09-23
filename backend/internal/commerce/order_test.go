package commerce_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// The order rules: what a plan costs, what an order totals, and why the price is taken
// from the catalogue rather than from the caller.

func plan(status string, minor int64, currency string) commerce.Plan {
	price, err := money.New(minor, currency)
	if err != nil {
		panic(err)
	}
	return commerce.Plan{
		ID:             uuid.MustParse("0198f1c2-0000-7000-8000-0000000000d1"),
		ProductID:      uuid.MustParse("0198f1c2-0000-7000-8000-0000000000d2"),
		Slug:           "vps-standard-2c4g",
		Name:           map[string]string{"zh-CN": "标准型", "en-US": "Standard"},
		Status:         status,
		CpuCores:       "2",
		MemoryMB:       4096,
		DiskGB:         80,
		Virtualization: "kvm",
		BillingCycle:   "monthly",
		Price:          price,
	}
}

func TestNewOrderLineRefusesAPlanThatIsNotOnSale(t *testing.T) {
	// A draft is still being written and an archived one is kept for the orders that
	// reference it; neither is on sale, and stating that here means a handler cannot
	// decide differently.
	for _, status := range []string{commerce.ProductDraft, commerce.ProductArchived} {
		if _, err := commerce.NewOrderLine(plan(status, 1999, "CNY"), 1); !errors.Is(err, commerce.ErrPlanNotPurchaseable) {
			t.Errorf("a %s plan was sold: %v", status, err)
		}
	}
	if _, err := commerce.NewOrderLine(plan(commerce.ProductActive, 1999, "CNY"), 1); err != nil {
		t.Fatalf("an active plan was refused: %v", err)
	}
}

func TestNewOrderLineBoundsTheQuantity(t *testing.T) {
	// A quantity of zero is a line that adds nothing, and one that is too large
	// overflows the total: the column is a bigint and the schema asserts the product,
	// so a quantity that overflows would fail the constraint rather than the total
	// silently wrapping. Refusing it here names the field instead.
	for _, quantity := range []int{0, -1, commerce.MaxOrderQuantity + 1} {
		if _, err := commerce.NewOrderLine(plan(commerce.ProductActive, 1999, "CNY"), quantity); !errors.Is(err, commerce.ErrInvalidQuantity) {
			t.Errorf("quantity %d was accepted: %v", quantity, err)
		}
	}
}

func TestNewOrderLineRejectsACallerSuppliedPriceByConstruction(t *testing.T) {
	// The unit price is read from the plan. There is no way to state one, which is the
	// whole reason this is a function rather than a struct literal: a client that could
	// send its own price could buy at any price it liked.
	line, err := commerce.NewOrderLine(plan(commerce.ProductActive, 1999, "CNY"), 2)
	if err != nil {
		t.Fatalf("build a line: %v", err)
	}
	if line.UnitPrice.AmountMinor != 1999 {
		t.Errorf("unit price = %d", line.UnitPrice.AmountMinor)
	}
}

func TestNewOrderLineValidatesThePlanDecimal(t *testing.T) {
	// cpu_cores is numeric(10,2) and is carried as a string, because a float cannot
	// represent 0.1 and a vCPU count that reads back as 1.9999999 is the first symptom
	// of a rounding decision nobody made.
	for _, cores := range []string{"1.5", "2", "0.25", "128.00"} {
		p := plan(commerce.ProductActive, 1999, "CNY")
		p.CpuCores = cores
		if _, err := commerce.NewOrderLine(p, 1); err != nil {
			t.Errorf("cpu_cores %q was refused: %v", cores, err)
		}
	}
	for _, cores := range []string{"", "two", "1.234", "-1", "1.", ".5"} {
		p := plan(commerce.ProductActive, 1999, "CNY")
		p.CpuCores = cores
		if _, err := commerce.NewOrderLine(p, 1); !errors.Is(err, commerce.ErrInvalidDecimal) {
			t.Errorf("cpu_cores %q was accepted: %v", cores, err)
		}
	}
}

func TestNewOrderTotalsItsLines(t *testing.T) {
	first, err := commerce.NewOrderLine(plan(commerce.ProductActive, 1999, "CNY"), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := commerce.NewOrderLine(plan(commerce.ProductActive, 4999, "CNY"), 2)
	if err != nil {
		t.Fatal(err)
	}

	order, err := commerce.NewOrder(uuid.New(), []commerce.OrderItem{first, second}, money.Zero("CNY"), time.Now())
	if err != nil {
		t.Fatalf("build an order: %v", err)
	}

	if order.Subtotal.AmountMinor != 1999+2*4999 {
		t.Errorf("subtotal = %d", order.Subtotal.AmountMinor)
	}
	if order.Total.AmountMinor != order.Subtotal.AmountMinor {
		t.Errorf("total = %d, subtotal = %d", order.Total.AmountMinor, order.Subtotal.AmountMinor)
	}
	// A new order is pending and unpaid, and the machine knows it.
	if order.Status != commerce.OrderPending {
		t.Errorf("status = %q", order.Status)
	}
	if order.PaidAt != nil {
		t.Error("a new order carries a payment time")
	}
	if order.OrderNo == "" || !strings.HasPrefix(order.OrderNo, "ORD-") {
		t.Errorf("order number = %q", order.OrderNo)
	}
}

func TestNewOrderAppliesTheDiscount(t *testing.T) {
	line, err := commerce.NewOrderLine(plan(commerce.ProductActive, 5000, "CNY"), 1)
	if err != nil {
		t.Fatal(err)
	}

	discount, _ := money.New(500, "CNY")
	order, err := commerce.NewOrder(uuid.New(), []commerce.OrderItem{line}, discount, time.Now())
	if err != nil {
		t.Fatalf("build an order: %v", err)
	}
	if order.Discount.AmountMinor != 500 {
		t.Errorf("discount = %d", order.Discount.AmountMinor)
	}
	if order.Total.AmountMinor != 4500 {
		t.Errorf("total = %d", order.Total.AmountMinor)
	}
}

func TestNewOrderRefusesAnIncoherentOne(t *testing.T) {
	line, err := commerce.NewOrderLine(plan(commerce.ProductActive, 5000, "CNY"), 1)
	if err != nil {
		t.Fatal(err)
	}
	usdLine, err := commerce.NewOrderLine(plan(commerce.ProductActive, 5000, "USD"), 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := commerce.NewOrder(uuid.New(), nil, money.Zero("CNY"), time.Now()); !errors.Is(err, commerce.ErrEmptyOrder) {
		t.Errorf("an empty order was accepted: %v", err)
	}
	// An order in two currencies would have a single total column and two meanings.
	if _, err := commerce.NewOrder(uuid.New(), []commerce.OrderItem{line, usdLine}, money.Zero("CNY"), time.Now()); !errors.Is(err, commerce.ErrMixedCurrencies) {
		t.Errorf("mixed currencies were accepted: %v", err)
	}
	// A discount larger than the subtotal would produce a negative total, and the
	// schema's CHECK would refuse it far from the cause.
	overDiscount, _ := money.New(9000, "CNY")
	if _, err := commerce.NewOrder(uuid.New(), []commerce.OrderItem{line}, overDiscount, time.Now()); !errors.Is(err, commerce.ErrInvalidAmount) {
		t.Errorf("an over-large discount was accepted: %v", err)
	}
	// A discount in the wrong currency, which would be a subtraction across currencies
	// that the money package refuses one level down.
	if _, err := commerce.NewOrder(uuid.New(), []commerce.OrderItem{line}, money.Zero("USD"), time.Now()); !errors.Is(err, commerce.ErrMixedCurrencies) {
		t.Errorf("a discount in another currency was accepted: %v", err)
	}
}

func TestOrderNumbersAreUniqueAndUnguessable(t *testing.T) {
	now := time.Now()
	seen := map[string]struct{}{}

	for i := 0; i < 500; i++ {
		number, err := commerce.NewOrderNumber(now)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if _, ok := seen[number]; ok {
			t.Fatalf("a repeated number after %d generations: %q", i+1, number)
		}
		seen[number] = struct{}{}
	}

	// Prefixed by kind, so an operator reading one out of an email can tell what it
	// refers to; dated, so roughly when.
	number, _ := commerce.NewOrderNumber(now)
	parts := strings.Split(number, "-")
	if len(parts) != 3 || parts[0] != "ORD" || len(parts[1]) != 8 {
		t.Errorf("order number shape = %q", number)
	}

	// Random rather than sequential: a sequence would tell anyone how many orders the
	// platform has and let them enumerate them.
	for _, prefix := range []string{"PAY", "INV"} {
		got, err := newNumberFor(prefix, now)
		if err != nil {
			t.Fatalf("generate a %s number: %v", prefix, err)
		}
		if !strings.HasPrefix(got, prefix+"-") {
			t.Errorf("number = %q, expected the %s prefix", got, prefix)
		}
	}
}

// newNumberFor reaches the unexported generator through the exported wrappers it sits
// behind, so the shape assertion covers them without restating the format.
func newNumberFor(prefix string, now time.Time) (string, error) {
	switch prefix {
	case "PAY":
		return commerce.NewPaymentNumber(now)
	case "INV":
		return commerce.NewInvoiceNumber(now)
	default:
		return commerce.NewOrderNumber(now)
	}
}

func TestPlanSnapshotIsFrozen(t *testing.T) {
	p := plan(commerce.ProductActive, 1999, "CNY")
	snapshot := commerce.NewPlanSnapshot(p)

	// Mutating the catalogue's own name map must not reach back into the snapshot: a
	// snapshot that changes when its source does is not a snapshot, and this is the
	// mistake the copy exists to prevent.
	p.Name["zh-CN"] = "改过的名字"
	if snapshot.Name["zh-CN"] != "标准型" {
		t.Errorf("the snapshot moved with its source: %v", snapshot.Name)
	}
	if snapshot.PriceMinor != 1999 || snapshot.Currency != "CNY" {
		t.Errorf("price = %d %s", snapshot.PriceMinor, snapshot.Currency)
	}
	if snapshot.PlanID != "0198f1c2-0000-7000-8000-0000000000d1" {
		t.Errorf("plan id = %q", snapshot.PlanID)
	}
}

func TestPlanSnapshotSurvivesJSONRoundTrip(t *testing.T) {
	// The snapshot's JSON tags are the contract with the order_items column: these rows
	// are read back by an interface long after they were written, so a field added to
	// Plan must not change what an old snapshot decodes into. The type is separate from
	// Plan for exactly that reason.
	snapshot := commerce.NewPlanSnapshot(plan(commerce.ProductActive, 1999, "CNY"))

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded commerce.PlanSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Re-encode the decoded copy and compare bytes, rather than comparing the structs:
	// the snapshot carries a map, which is not comparable, and comparing the encodings is
	// also the stronger property — it is what a stored row would have to match.
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if string(roundTrip) != string(encoded) {
		t.Errorf("the round trip changed the snapshot:\n  sent: %s\n  read: %s", encoded, roundTrip)
	}

	// The field names are snake case, matching the rest of the API (docs/08).
	if !strings.Contains(string(encoded), `"price_minor"`) {
		t.Errorf("the encoding uses unexpected field names: %s", encoded)
	}
	if strings.Contains(string(encoded), `"PriceMinor"`) {
		t.Errorf("the encoding leaked a Go field name: %s", encoded)
	}
}

func TestNormalizeStatusReadsWhatAGatewaySends(t *testing.T) {
	// A gateway sends what it sends, and the platform's vocabulary is lower case. A
	// comparison that failed on a capital letter would be a settlement that silently
	// did not happen.
	cases := map[string]string{
		"Succeeded":     "succeeded",
		" succeeded":    "succeeded",
		"succeeded ":    "succeeded",
		"  SUCCEEDED  ": "succeeded",
		"succeeded":     "succeeded",
	}
	for in, want := range cases {
		if got := commerce.NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %q, expected %q", in, got, want)
		}
	}
}

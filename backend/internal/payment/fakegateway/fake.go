// Package fakegateway is a payment gateway that answers locally and signs its own
// callbacks.
//
// It exists so that the whole payment path — creating an intent, receiving a signed
// callback, settling it exactly once — can be built and tested before a real gateway
// contract exists. Phase 6's Gate is precisely this: browse → order → fake payment →
// provisioned. Replacing it later is a matter of implementing `payment.Gateway` again,
// which is what the port is for.
//
// It is not a mock in the test-only sense: it implements the same interface a real
// gateway will, it signs its callbacks the way a real one does, and it is wired into
// the server as the default gateway in every environment until a real one is
// configured. What it does not do is talk to a bank.
package fakegateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment"
)

// SignatureHeader is the header the gateway signs its callbacks in.
//
// Exported because the webhook handler reads it, and because a test that builds a
// callback needs the same name the handler does.
const SignatureHeader = "X-Fake-Signature"

// signaturePrefix is what the header's value begins with, so that a signature scheme
// can be replaced without guessing which scheme a header belongs to.
const signaturePrefix = "sha256="

// gatewayName is the value stored on a payment row.
const gatewayName = "fake"

// SignaturePrefix is what a signature header value begins with.
func SignaturePrefix() string { return signaturePrefix }

// statuses the fake gateway reports, in its own words. They are lower case already,
// which is what the settlement's normalisation expects — and the tests that feed a
// callback through in mixed case are how that is proven.
const (
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
)

// Fake is a local gateway.
type Fake struct {
	secret []byte
	now    func() time.Time
}

// New builds a gateway that signs with the given secret.
//
// The secret is shared with whoever verifies the callbacks, which in this codebase is
// the same process; with a real gateway it is the secret the provider issued, and it
// is configured rather than generated.
func New(secret string) *Fake {
	if strings.TrimSpace(secret) == "" {
		// A gateway that signs with an empty secret verifies anything signed with one,
		// which is no verification at all. Refusing it here is cheaper than a webhook
		// endpoint that accepts forgeries.
		panic("fakegateway: a signing secret is required")
	}
	return &Fake{secret: []byte(secret), now: time.Now}
}

// Name implements payment.Gateway.
func (*Fake) Name() string { return gatewayName }

// SignatureHeader implements payment.Gateway.
func (*Fake) SignatureHeader() string { return SignatureHeader }

// CreateIntent answers with an identifier derived from the platform's own reference.
//
// The identifier is derived rather than random, on purpose: the same payment number
// asking twice produces the same gateway payment id, so a retry cannot become a second
// payment. That is the property the real gateway's idempotency gives, reproduced here
// so the tests exercise the code that depends on it.
func (f *Fake) CreateIntent(_ context.Context, req payment.CreateIntentRequest) (payment.Intent, error) {
	if strings.TrimSpace(req.PaymentNo) == "" {
		return payment.Intent{}, fmt.Errorf("%w: no payment reference", payment.ErrRejected)
	}
	if req.Amount.AmountMinor <= 0 {
		return payment.Intent{}, fmt.Errorf("%w: amount is %d", payment.ErrRejected, req.Amount.AmountMinor)
	}
	if req.Amount.Currency == "" {
		return payment.Intent{}, fmt.Errorf("%w: no currency", payment.ErrRejected)
	}

	mac := hmac.New(sha256.New, f.secret)
	_, _ = mac.Write([]byte(req.PaymentNo))
	id := "fake_" + hex.EncodeToString(mac.Sum(nil))[:24]

	expires := f.now().Add(2 * time.Hour)
	return payment.Intent{
		GatewayPaymentID: id,
		PayURL:           "/pay/" + id,
		ExpiresAt:        &expires,
	}, nil
}

// NotificationBody builds the body a callback would carry.
//
// It is on the gateway rather than in a test helper because it is the gateway's own
// format, and a test that built the body itself would be verifying that it agrees with
// itself rather than that the handler understands the gateway.
func (*Fake) NotificationBody(n payment.Notification) ([]byte, error) {
	payload := map[string]any{
		"gateway_payment_id": n.GatewayPaymentID,
		"status":             n.Status,
		"amount_minor":       n.Amount.AmountMinor,
		"currency":           string(n.Amount.Currency),
	}
	for key, value := range n.Payload {
		payload[key] = value
	}
	return json.Marshal(payload)
}

// Sign returns the header value a callback for this body would carry.
func (f *Fake) Sign(body []byte) string {
	mac := hmac.New(sha256.New, f.secret)
	_, _ = mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// Verify parses a callback body and checks its signature.
func (f *Fake) Verify(body []byte, signature string) (payment.Notification, error) {
	if !strings.HasPrefix(signature, signaturePrefix) {
		return payment.Notification{}, fmt.Errorf("%w: the header does not name a scheme", payment.ErrSignatureInvalid)
	}

	expected := f.Sign(body)
	given := signature
	if !hmac.Equal([]byte(expected), []byte(given)) {
		// hmac.Equal is constant time, so a caller probing the signature learns
		// nothing from how long a wrong one takes to be rejected.
		return payment.Notification{}, payment.ErrSignatureInvalid
	}

	var payload struct {
		GatewayPaymentID string         `json:"gateway_payment_id"`
		Status           string         `json:"status"`
		AmountMinor      int64          `json:"amount_minor"`
		Currency         string         `json:"currency"`
		Extra            map[string]any `json:"-"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payment.Notification{}, fmt.Errorf("%w: %w", payment.ErrMalformedCallback, err)
	}
	if strings.TrimSpace(payload.GatewayPaymentID) == "" {
		return payment.Notification{}, fmt.Errorf("%w: no gateway payment id", payment.ErrMalformedCallback)
	}

	// The rest of the body is carried through untouched, so a gateway field this
	// version does not know about is stored rather than discarded.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return payment.Notification{}, fmt.Errorf("%w: %w", payment.ErrMalformedCallback, err)
	}
	delete(raw, "gateway_payment_id")
	delete(raw, "status")
	delete(raw, "amount_minor")
	delete(raw, "currency")

	amount, err := money.New(payload.AmountMinor, strings.ToUpper(payload.Currency))
	if err != nil {
		return payment.Notification{}, fmt.Errorf("%w: %w", payment.ErrMalformedCallback, err)
	}

	return payment.Notification{
		GatewayPaymentID: payload.GatewayPaymentID,
		Status:           strings.ToLower(strings.TrimSpace(payload.Status)),
		Amount:           amount,
		Payload:          raw,
	}, nil
}

// Statuses the fake can report, exported so a test can use the gateway's words rather
// than the platform's.
var (
	StatusSucceeded = statusSucceeded
	StatusFailed    = statusFailed
)

// ErrUnsupportedStatus is returned by Notify for a status the gateway does not emit.
var ErrUnsupportedStatus = errors.New("fakegateway: unsupported status")

// Notify is a convenience that builds a signed callback in one step.
//
// It exists for the end-to-end and Gate tests, which need a callback that is
// indistinguishable from one the gateway would really have sent — correct bytes, correct
// signature — and would otherwise repeat the body-then-sign sequence everywhere.
func (f *Fake) Notify(gatewayPaymentID, status string, amount money.Money) (body []byte, signature string, err error) {
	if status != statusSucceeded && status != statusFailed {
		return nil, "", fmt.Errorf("%w: %q", ErrUnsupportedStatus, status)
	}

	encoded, err := f.NotificationBody(payment.Notification{
		GatewayPaymentID: gatewayPaymentID,
		Status:           status,
		Amount:           amount,
	})
	if err != nil {
		return nil, "", fmt.Errorf("fakegateway: build the body: %w", err)
	}
	return encoded, f.Sign(encoded), nil
}

// RandomID returns an identifier that looks like one of this gateway's own.
func RandomID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "fake_" + hex.EncodeToString(buf)
}

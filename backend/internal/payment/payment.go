// Package payment is the port a payment gateway sits behind.
//
// It is deliberately separate from `internal/provider`, which is the compute Provider
// contract of docs/06. The two are both external integrations and nothing else: a
// gateway is asked for money and answers with a signed notification, a compute provider
// is asked for virtual machines and answers with state. Forcing one into the other's
// interface would make both of them lie about what they are.
//
// The interface is what the settlement depends on, so the fake gateway in the
// `fakegateway` package and any real one are interchangeable without touching the code
// that decides what to do with money.
package payment

import (
	"context"
	"errors"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
)

// Errors reported by a gateway.
var (
	// ErrSignatureInvalid reports a callback whose signature does not match.
	ErrSignatureInvalid = errors.New("payment: callback signature is invalid")
	// ErrMalformedCallback reports a callback that cannot be parsed.
	ErrMalformedCallback = errors.New("payment: callback cannot be parsed")
	// ErrGatewayUnavailable reports that the gateway could not be reached.
	ErrGatewayUnavailable = errors.New("payment: gateway is unreachable")
	// ErrRejected reports that the gateway refused the request.
	ErrRejected = errors.New("payment: gateway rejected the request")
)

// CreateIntentRequest is what the platform asks a gateway for.
type CreateIntentRequest struct {
	// PaymentNo is the platform's reference. The gateway echoes it back, and the
	// platform's own identifier is what makes a retry produce the same intent rather
	// than a second one.
	PaymentNo   string
	Amount      money.Money
	Description string
}

// Intent is a gateway's answer to a payment request.
type Intent struct {
	// GatewayPaymentID is the gateway's identifier for this attempt. It is what a
	// callback names, and the unique index over it is what stops one gateway payment
	// from becoming two payment rows.
	GatewayPaymentID string
	// PayURL is where the customer completes the payment.
	PayURL string
	// ExpiresAt is when the gateway stops honouring the intent. A nil value means the
	// gateway did not say.
	ExpiresAt *time.Time
}

// Notification is a parsed, verified callback.
type Notification struct {
	// GatewayPaymentID names the payment the callback is about.
	GatewayPaymentID string
	// Status is the gateway's word for what happened, normalised to lower case. It is
	// the gateway's vocabulary and not the platform's: mapping it is the settlement's
	// job, because only the settlement knows what the platform does about it.
	Status string
	// Amount is what the gateway says was paid. The settlement compares it with the
	// platform's own record, and a disagreement is a conflict rather than a replay.
	Amount money.Money
	// Payload is the callback as received, stored with the payment so that a dispute
	// has the original bytes' meaning attached to it.
	Payload map[string]any
}

// Gateway is one payment provider.
type Gateway interface {
	// Name identifies the gateway. It is stored with the payment and is the key a
	// webhook route is mounted under.
	Name() string

	// SignatureHeader is the header this gateway carries its signature in. It belongs
	// to the gateway rather than to the handler, because the handler should not have to
	// know that one provider calls it X-Signature and another X-Hub-Signature.
	SignatureHeader() string

	// CreateIntent asks the gateway to take money.
	CreateIntent(ctx context.Context, req CreateIntentRequest) (Intent, error)

	// Verify parses a callback body and checks that it is authentic.
	//
	// It takes the raw bytes rather than a decoded struct, deliberately: a signature is
	// computed over bytes, and verifying one over a re-serialised form would verify
	// things that were never signed. The classic failure is two different byte strings
	// that parse to the same object.
	Verify(body []byte, signature string) (Notification, error)
}

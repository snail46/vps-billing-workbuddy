package fakegateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment/fakegateway"
)

// These tests cover the gateway's own rules: that it signs bytes and not a
// re-serialised form, that a tampered body is refused, and that the same payment
// reference always yields the same identifier — which is the property the settlement's
// idempotency rests on.

const testSecret = "a-test-signing-secret"

func testAmount(t *testing.T, minor int64) money.Money {
	t.Helper()
	value, err := money.New(minor, "CNY")
	if err != nil {
		t.Fatalf("build amount: %v", err)
	}
	return value
}

func TestCreateIntentDerivesTheIdentifierFromThePaymentNumber(t *testing.T) {
	gateway := fakegateway.New(testSecret)
	req := payment.CreateIntentRequest{
		PaymentNo:   "PAY-20260923-AAAA",
		Amount:      testAmount(t, 9900),
		Description: "an order",
	}

	first, err := gateway.CreateIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("create an intent: %v", err)
	}
	second, err := gateway.CreateIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("create the same intent again: %v", err)
	}

	// The same reference asking twice is the same payment. A random identifier here
	// would mean a retry creates a second gateway payment, and the platform would then
	// hold two records for one attempt at someone's money.
	if first.GatewayPaymentID != second.GatewayPaymentID {
		t.Errorf("a retry produced a different identifier: %q and %q",
			first.GatewayPaymentID, second.GatewayPaymentID)
	}
	if !strings.HasPrefix(first.GatewayPaymentID, "fake_") {
		t.Errorf("identifier = %q", first.GatewayPaymentID)
	}
	if first.PayURL == "" {
		t.Error("no payment URL")
	}
	if first.ExpiresAt == nil || !first.ExpiresAt.After(time.Now()) {
		t.Error("the intent is already expired")
	}
}

func TestCreateIntentRefusesAnUnusableRequest(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	cases := map[string]payment.CreateIntentRequest{
		"no payment reference": {
			Amount: testAmount(t, 100),
		},
		"zero amount": {
			PaymentNo: "PAY-1",
			Amount:    testAmount(t, 0),
		},
		"no currency": {
			PaymentNo: "PAY-1",
			Amount:    money.Money{AmountMinor: 100},
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := gateway.CreateIntent(context.Background(), req); !errors.Is(err, payment.ErrRejected) {
				t.Errorf("the request was accepted: %v", err)
			}
		})
	}
}

func TestNewRefusesAnEmptySecret(t *testing.T) {
	// A gateway that signs with an empty secret verifies anything signed with one,
	// which is no verification at all.
	defer func() {
		if recover() == nil {
			t.Error("an empty secret was accepted")
		}
	}()
	fakegateway.New("   ")
}

func TestVerifyAcceptsASignedCallback(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	body, signature, err := gateway.Notify("fake_abc123", "succeeded", testAmount(t, 9900))
	if err != nil {
		t.Fatalf("build a notification: %v", err)
	}

	notification, err := gateway.Verify(body, signature)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if notification.GatewayPaymentID != "fake_abc123" {
		t.Errorf("gateway payment id = %q", notification.GatewayPaymentID)
	}
	if notification.Status != "succeeded" {
		t.Errorf("status = %q", notification.Status)
	}
	if notification.Amount.AmountMinor != 9900 || notification.Amount.Currency != "CNY" {
		t.Errorf("amount = %+v", notification.Amount)
	}
}

func TestVerifyRefusesATamperedBody(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	body, signature, err := gateway.Notify("fake_abc123", "succeeded", testAmount(t, 9900))
	if err != nil {
		t.Fatalf("build a notification: %v", err)
	}

	// The amount is what an attacker would change: a callback for 9900 accepted as one
	// for 99 is the whole attack.
	tampered := strings.Replace(string(body), "9900", "99", 1)
	if tampered == string(body) {
		t.Fatal("the tampering changed nothing; the test proves nothing")
	}

	if _, err := gateway.Verify([]byte(tampered), signature); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("a tampered body was accepted: %v", err)
	}
}

func TestVerifyRefusesAWrongSecret(t *testing.T) {
	body, signature, err := fakegateway.New(testSecret).Notify(
		"fake_abc123", "succeeded", testAmount(t, 9900))
	if err != nil {
		t.Fatalf("build a notification: %v", err)
	}

	// Signed by a gateway with a different secret: the same shape, the wrong key.
	other := fakegateway.New("a-different-secret")
	if _, err := other.Verify(body, signature); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("a callback from another gateway was accepted: %v", err)
	}
}

func TestVerifyRefusesAHeaderWithoutAScheme(t *testing.T) {
	gateway := fakegateway.New(testSecret)
	body, signature, err := gateway.Notify("fake_abc123", "succeeded", testAmount(t, 100))
	if err != nil {
		t.Fatalf("build a notification: %v", err)
	}

	// The prefix says which scheme produced the value. Without it the verifier would
	// have to guess, and a guess that accepts the wrong scheme is how a signature
	// check becomes decorative.
	bare := strings.TrimPrefix(signature, fakegateway.SignaturePrefix())
	if !strings.HasPrefix(signature, fakegateway.SignaturePrefix()) {
		t.Fatalf("the signature does not carry a scheme prefix: %q", signature)
	}
	if _, err := gateway.Verify(body, bare); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Errorf("a signature without a scheme was accepted: %v", err)
	}

	if _, err := gateway.Verify(body, ""); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Errorf("an empty signature was accepted: %v", err)
	}
}

func TestVerifyRefusesAMalformedBody(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	// Signed by the gateway but not parseable. Only someone with the secret could have
	// produced these bytes, which is what makes this a malformed callback rather than a
	// forged one — and it has to be refused all the same.
	malformed := []byte("not json at all")
	if _, err := gateway.Verify(malformed, gateway.Sign(malformed)); !errors.Is(err, payment.ErrMalformedCallback) {
		t.Errorf("a malformed body produced %v", err)
	}

	// Parseable, but with nothing to settle against.
	empty := []byte(`{"gateway_payment_id":"  "}`)
	if _, err := gateway.Verify(empty, gateway.Sign(empty)); !errors.Is(err, payment.ErrMalformedCallback) {
		t.Errorf("a body with no payment id produced %v", err)
	}

	// A currency that cannot be an ISO code. A gateway's own lower-case code is
	// normalised on the way in — being strict about the case of someone else's field
	// would break a settlement for nothing — but a code with the wrong shape is a
	// callback that cannot be compared with the platform's record.
	unknowable := []byte(`{"gateway_payment_id":"fake_x","amount_minor":1,"currency":"CN"}`)
	if _, err := gateway.Verify(unknowable, gateway.Sign(unknowable)); !errors.Is(err, payment.ErrMalformedCallback) {
		t.Errorf("a currency of the wrong shape produced %v", err)
	}

	// A gateway reporting no amount cannot be settled, because the settlement compares
	// what the gateway says was paid with what the platform asked for.
	amountless := []byte(`{"gateway_payment_id":"fake_x","status":"succeeded"}`)
	if _, err := gateway.Verify(amountless, gateway.Sign(amountless)); !errors.Is(err, payment.ErrMalformedCallback) {
		t.Errorf("a body with no amount produced %v", err)
	}
}

func TestVerifyKeepsUnknownFields(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	// A gateway field this version does not know about is stored rather than dropped,
	// because a dispute is settled with what the gateway actually sent.
	body := []byte(`{"gateway_payment_id":"fake_x","status":"succeeded","amount_minor":100,"currency":"CNY","gateway_fee_minor":3,"memo":"paid by card"}`)
	signed := gateway.Sign(body)

	notification, err := gateway.Verify(body, signed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if notification.Payload["gateway_fee_minor"] == nil || notification.Payload["memo"] == nil {
		t.Errorf("unknown fields were dropped: %v", notification.Payload)
	}
	// The fields that became structure are not duplicated into the payload.
	if _, ok := notification.Payload["status"]; ok {
		t.Errorf("status was duplicated into the payload: %v", notification.Payload)
	}
}

func TestNotifyRefusesAStatusTheGatewayDoesNotEmit(t *testing.T) {
	gateway := fakegateway.New(testSecret)

	// A test that could send any status would be testing the platform's handling of a
	// callback the gateway cannot produce.
	if _, _, err := gateway.Notify("fake_x", "abandoned", testAmount(t, 100)); !errors.Is(err, fakegateway.ErrUnsupportedStatus) {
		t.Errorf("an unsupported status was accepted: %v", err)
	}
}

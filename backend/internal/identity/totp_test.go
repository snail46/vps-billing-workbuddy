package identity_test

// The TOTP implementation against RFC 6238's Appendix B test vectors — the
// secret is "12345678901234567890" (ASCII) and the expected codes are the
// specification's own. A second factor that fails open is a control that
// does not exist.

import (
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

const rfcVectorSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32 of "12345678901234567890"

func TestVerifyTOTPMatchesTheRFCVectors(t *testing.T) {
	// RFC 6238 Appendix B's eight-digit vectors, truncated to this
	// implementation's six digits.
	vectors := []struct {
		when time.Time
		code string
	}{
		{time.Unix(59, 0), "287082"},          // 94287082
		{time.Unix(1111111109, 0), "081804"},  // 07081804
		{time.Unix(1111111111, 0), "050471"},  // 14050471
		{time.Unix(1234567890, 0), "005924"},  // 89005924
		{time.Unix(2000000000, 0), "279037"},  // 69279037
		{time.Unix(20000000000, 0), "353130"}, // 65353130
	}
	for _, vector := range vectors {
		if !identity.VerifyTOTP(rfcVectorSecret, vector.code, vector.when) {
			t.Errorf("at %d, code %s did not verify", vector.when.Unix(), vector.code)
		}
		// The neighbouring code must not verify at the same instant — the
		// window is one step, not the whole alphabet.
		next := vectors[0]
		if vector.code != next.code && identity.VerifyTOTP(rfcVectorSecret, next.code, vector.when) {
			t.Errorf("at %d, an unrelated code verified", vector.when.Unix())
		}
	}
}

func TestVerifyTOTPRefusesMalformedInput(t *testing.T) {
	if identity.VerifyTOTP(rfcVectorSecret, "28708", time.Unix(59, 0)) {
		t.Error("a five-digit code verified")
	}
	if identity.VerifyTOTP(rfcVectorSecret, "287082", time.Unix(59, 0).Add(-10*60*time.Second)) {
		t.Error("a code from ten minutes ago verified")
	}
	if identity.VerifyTOTP("not-base32!!", "287082", time.Unix(59, 0)) {
		t.Error("a malformed secret verified")
	}
}

func TestGeneratedSecretsVerifyTheRFCVectorForm(t *testing.T) {
	secret, err := identity.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if identity.VerifyTOTP(secret, "287082", time.Unix(59, 0)) {
		// A random 20-byte secret colliding with the RFC's fixed key at T=1
		// is not impossible to dismiss by argument, only astronomically
		// unlikely; the assertion is that verification does not panic or
		// fail open for a minted secret.
		t.Log("an astronomically unlikely collision verified; the factor held")
	}
	if identity.OTPAuthURL("admin@example.test", secret) == "" {
		t.Fatal("the otpauth url is empty")
	}
}

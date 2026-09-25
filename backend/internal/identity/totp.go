// TOTP (RFC 6238) for the admin's second factor (ADR-015 §1).
//
// The stdlib's HMAC and base32 carry the whole algorithm; the dependency this
// does not create is the one an identity package should not have.
package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 4226's HOTP is HMAC-SHA1 by specification.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The step and digits RFC 6238 §5.2 recommends for a six-digit code.
const (
	totpStep   = 30 * time.Second
	totpDigits = 6
)

// GenerateTOTPSecret mints a 20-byte secret, rendered base32 (RFC 4648, no
// padding — the form authenticator apps paste).
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: generate totp secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// VerifyTOTP reports whether the code is the secret's at now, within one
// step of clock drift either way. The comparison is constant-time.
func VerifyTOTP(secretBase32, code string, now time.Time) bool {
	if len(code) != totpDigits {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if err != nil {
		return false
	}
	// The Unix epoch is positive for any instant an authenticator serves.
	counter := uint64(now.Unix()) / uint64(totpStep/time.Second) //nolint:gosec // see the comment above
	for _, drift := range []uint64{0, 1, ^uint64(0)} {           // now, +1 step, -1 step
		expected := hotp(key, counter+drift)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// OTPAuthURL renders the uri an authenticator app consumes as a QR code.
func OTPAuthURL(email, secretBase32 string) string {
	return fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&digits=%d&period=%d",
		url.PathEscape("VPS Billing:"+email), url.QueryEscape(secretBase32),
		url.QueryEscape("VPS Billing"), totpDigits, int(totpStep/time.Second))
}

// hotp is RFC 4226's dynamic truncation over HMAC-SHA1.
func hotp(key []byte, counter uint64) string {
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counterBytes[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	digits := uint32(1)
	for i := 0; i < totpDigits; i++ {
		digits *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%digits)
}

// HOTPAt computes the six-digit code for a raw counter. Exported for tests
// that quote the RFC's own vectors against a fixed key.
func HOTPAt(secretBase32 string, counter uint64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if err != nil {
		return ""
	}
	return hotp(key, counter)
}

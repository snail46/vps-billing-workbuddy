package usersurface

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
)

// numberEncoding is Crockford's base32 without the ambiguous letters, the
// same alphabet the commerce numbers use: these numbers are typed by hand.
var numberEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func randomTicketSuffix(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("usersurface: generate a ticket number: %w", err)
	}
	return numberEncoding.EncodeToString(buf), nil
}

// Package identity owns credentials, sessions and authorisation: the objects and
// rules behind "who is this, and what may they do".
//
// The package is deliberately unaware of HTTP, of the database driver and of
// Redis. Those live in internal/httpapi and in the queries the service is given,
// so the rules here can be tested without a server, a database or a container
// runtime — which matters, because per ADR-003 this machine has none of them.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrPasswordHashInvalid reports a stored hash that cannot be interpreted.
//
// It is returned rather than treated as "wrong password" on purpose: a malformed
// hash means the stored credential is unusable, and reporting it as a failed login
// would send a user to reset a password that was never going to work, while hiding
// a corrupt row from operators.
var ErrPasswordHashInvalid = errors.New("identity: password hash is malformed")

// MinPasswordLength is the shortest password accepted at registration.
//
// No specification in the repository fixes a minimum, so this is a stated default.
// Twelve characters rather than eight: length is the property that actually
// resists offline cracking, and the cost of typing a longer password is paid once.
const MinPasswordLength = 12

// PasswordParams are the argon2id cost parameters.
//
// Memory is measured in KiB, matching both the algorithm and the `m=` field of the
// encoded hash.
type PasswordParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultPasswordParams returns the current cost parameters.
//
// These follow the OWASP guidance for argon2id (64 MiB, three iterations, four
// lanes). They are a starting point rather than a constant: because the parameters
// are stored inside every hash, they can be raised later, and a login against an
// older hash reports NeedsRehash so the password is re-hashed on the way through.
func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Bounds on parameters read back out of a stored hash.
//
// Verification derives its cost from the stored string, which means a corrupt row —
// or a row written by something other than this code — could otherwise ask for
// terabytes of memory and turn a login into an out-of-memory kill. The values are
// generous multiples of the defaults so that a deliberate future increase still
// fits, while an absurd one is refused instead of attempted.
//
// The salt and key bounds are applied to the decoded byte lengths rather than to
// anything in the string: the encoded form carries the cost parameters only, so the
// lengths are a property of what was actually stored.
const (
	maxHashMemory      = 1 << 20 // 1 GiB
	maxHashIterations  = 20
	maxHashParallelism = 64
	minHashSaltLength  = 8
	maxHashSaltLength  = 64
	minHashKeyLength   = 16
	maxHashKeyLength   = 128
)

// PasswordVerification is the outcome of checking a password.
type PasswordVerification struct {
	// Matches reports whether the password produced the stored hash.
	Matches bool
	// NeedsRehash reports that the stored hash used different parameters from the
	// hasher's current ones, so a caller that has just authenticated the user
	// should replace it. It is reported even when Matches is false, because the
	// caller decides what to do with it.
	NeedsRehash bool
}

// PasswordHasher hashes and verifies passwords with argon2id.
//
// The zero value is not usable; construct one with NewPasswordHasher.
type PasswordHasher struct {
	params PasswordParams
	// dummy is a valid hash of a random value, used to spend the same work on an
	// unknown account as on a known one. See VerifyUnknownAccount.
	dummy string
}

// NewPasswordHasher returns a hasher using the given parameters.
func NewPasswordHasher(params PasswordParams) (*PasswordHasher, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}

	dummy, err := hashPassword(params, randomBytes(int(params.SaltLength)),
		base64.RawStdEncoding.EncodeToString(randomBytes(32)))
	if err != nil {
		return nil, fmt.Errorf("identity: building the dummy hash: %w", err)
	}

	return &PasswordHasher{params: params, dummy: dummy}, nil
}

// validate checks a complete parameter set, including the salt and key lengths.
func (p PasswordParams) validate() error {
	if err := p.validateCost(); err != nil {
		return err
	}
	switch {
	case p.SaltLength < minHashSaltLength || p.SaltLength > maxHashSaltLength:
		return fmt.Errorf("identity: password salt length %d is outside %d..%d",
			p.SaltLength, minHashSaltLength, maxHashSaltLength)
	case p.KeyLength < minHashKeyLength || p.KeyLength > maxHashKeyLength:
		return fmt.Errorf("identity: password key length %d is outside %d..%d",
			p.KeyLength, minHashKeyLength, maxHashKeyLength)
	}
	return nil
}

// validateCost checks only the cost parameters.
//
// It is separate from validate because the encoded hash carries the cost parameters
// but not the salt and key lengths: those are properties of the decoded bytes. A
// parsed hash therefore has to be checked for cost before the lengths are known,
// and for lengths once they are.
func (p PasswordParams) validateCost() error {
	switch {
	case p.Memory == 0 || p.Memory > maxHashMemory:
		return fmt.Errorf("identity: password memory cost %d is outside 1..%d KiB", p.Memory, maxHashMemory)
	case p.Iterations == 0 || p.Iterations > maxHashIterations:
		return fmt.Errorf("identity: password iteration count %d is outside 1..%d", p.Iterations, maxHashIterations)
	case p.Parallelism == 0 || p.Parallelism > maxHashParallelism:
		return fmt.Errorf("identity: password parallelism %d is outside 1..%d", p.Parallelism, maxHashParallelism)
	}
	return nil
}

// Hash returns a PHC-encoded argon2id hash of the password.
//
// The encoding carries the parameters and the salt, so verification needs nothing
// but the string. That is what makes the parameters changeable without a schema
// change and without a second column.
func (h *PasswordHasher) Hash(password string) (string, error) {
	return hashPassword(h.params, randomBytes(int(h.params.SaltLength)), password)
}

func hashPassword(params PasswordParams, salt []byte, password string) (string, error) {
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	encoding := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.Memory,
		params.Iterations,
		params.Parallelism,
		encoding.EncodeToString(salt),
		encoding.EncodeToString(key),
	), nil
}

// Verify checks a password against a stored hash.
func (h *PasswordHasher) Verify(password, encoded string) (PasswordVerification, error) {
	stored, err := parsePasswordHash(encoded)
	if err != nil {
		return PasswordVerification{}, err
	}

	key := argon2.IDKey(
		[]byte(password),
		stored.salt,
		stored.params.Iterations,
		stored.params.Memory,
		stored.params.Parallelism,
		stored.params.KeyLength,
	)

	// Constant time: a comparison that returns early leaks how much of a guessed
	// hash was correct, which is enough to reconstruct it byte by byte.
	matches := subtle.ConstantTimeCompare(key, stored.key) == 1

	return PasswordVerification{
		Matches: matches,
		// Any difference in the parameter set means the credential was stored under
		// an older cost, so a successful login is the moment to replace it.
		NeedsRehash: matches && stored.params != h.params,
	}, nil
}

// VerifyUnknownAccount performs the same work as Verify without a stored hash.
//
// It exists so that a login attempt against an address that is not registered
// costs the same as one against an address that is. Without it, response time
// reports whether an address exists, which turns the login endpoint into a
// membership oracle. The caller discards the result.
func (h *PasswordHasher) VerifyUnknownAccount(password string) {
	_, _ = h.Verify(password, h.dummy)
}

// DummyHash exposes the hash VerifyUnknownAccount uses. It exists for tests that
// need a syntactically valid hash which no password matches.
func (h *PasswordHasher) DummyHash() string { return h.dummy }

type parsedHash struct {
	params PasswordParams
	salt   []byte
	key    []byte
}

func parsePasswordHash(encoded string) (parsedHash, error) {
	fields := strings.Split(encoded, "$")
	// Leading empty field, algorithm, version, parameters, salt, key.
	if len(fields) != 6 || fields[0] != "" {
		return parsedHash{}, fmt.Errorf("%w: expected six $ separated fields, got %d", ErrPasswordHashInvalid, len(fields))
	}
	if fields[1] != "argon2id" {
		return parsedHash{}, fmt.Errorf("%w: unsupported algorithm %q", ErrPasswordHashInvalid, fields[1])
	}
	version, err := strconv.Atoi(strings.TrimPrefix(fields[2], "v="))
	if err != nil || version != argon2.Version {
		return parsedHash{}, fmt.Errorf("%w: unsupported version %q", ErrPasswordHashInvalid, fields[2])
	}

	params, err := parseParams(fields[3])
	if err != nil {
		return parsedHash{}, err
	}
	if err := params.validateCost(); err != nil {
		// Both are wrapped: the sentinel is what callers test for, and the inner
		// error carries which bound was violated. Go allows more than one %w.
		return parsedHash{}, fmt.Errorf("%w: %w", ErrPasswordHashInvalid, err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil {
		return parsedHash{}, fmt.Errorf("%w: salt is not valid unpadded base64", ErrPasswordHashInvalid)
	}

	key, err := base64.RawStdEncoding.DecodeString(fields[5])
	if err != nil {
		return parsedHash{}, fmt.Errorf("%w: key is not valid unpadded base64", ErrPasswordHashInvalid)
	}

	// The lengths are only knowable from the decoded bytes, so they are recorded
	// here. That makes a parsed hash a complete parameter set: verification uses its
	// key length, and comparing it against the hasher's own parameters is what
	// reports NeedsRehash.
	if err := checkLength("salt", len(salt), minHashSaltLength, maxHashSaltLength); err != nil {
		return parsedHash{}, err
	}
	if err := checkLength("key", len(key), minHashKeyLength, maxHashKeyLength); err != nil {
		return parsedHash{}, err
	}
	params.SaltLength = uint32(len(salt)) //nolint:gosec // checkLength bounded it to at most 64.
	params.KeyLength = uint32(len(key))   //nolint:gosec // checkLength bounded it to at most 128.

	if err := params.validate(); err != nil {
		return parsedHash{}, fmt.Errorf("%w: %w", ErrPasswordHashInvalid, err)
	}

	return parsedHash{params: params, salt: salt, key: key}, nil
}

// checkLength reports a decoded salt or key whose size is outside the accepted
// range.
//
// It exists so the bound is applied before the length is narrowed to the uint32 the
// algorithm takes, rather than after, which is what makes the conversion provably
// safe instead of merely true on this platform.
func checkLength(label string, length, minimum, maximum int) error {
	if length < minimum || length > maximum {
		return fmt.Errorf("%w: %s length %d is outside %d..%d",
			ErrPasswordHashInvalid, label, length, minimum, maximum)
	}
	return nil
}

func parseParams(field string) (PasswordParams, error) {
	var params PasswordParams
	seen := make(map[string]bool, 3)

	for _, pair := range strings.Split(field, ",") {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			return PasswordParams{}, fmt.Errorf("%w: parameter %q is not name=value", ErrPasswordHashInvalid, pair)
		}
		number, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return PasswordParams{}, fmt.Errorf("%w: parameter %q is not a number", ErrPasswordHashInvalid, pair)
		}

		switch name {
		case "m":
			params.Memory = uint32(number)
		case "t":
			params.Iterations = uint32(number)
		case "p":
			if number > 255 {
				return PasswordParams{}, fmt.Errorf("%w: parallelism %d does not fit a lane count", ErrPasswordHashInvalid, number)
			}
			params.Parallelism = uint8(number)
		default:
			return PasswordParams{}, fmt.Errorf("%w: unknown parameter %q", ErrPasswordHashInvalid, name)
		}
		seen[name] = true
	}

	for _, required := range []string{"m", "t", "p"} {
		if !seen[required] {
			return PasswordParams{}, fmt.Errorf("%w: parameter %q is missing", ErrPasswordHashInvalid, required)
		}
	}

	return params, nil
}

func randomBytes(size int) []byte {
	buffer := make([]byte, size)
	// crypto/rand.Read never returns an error on any platform this runs on; the
	// signature returns one for API compatibility. There is nothing to recover to,
	// so a failure is fatal rather than a silent weakening of the salt.
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("identity: reading random bytes: %v", err))
	}
	return buffer
}

package identity

import (
	"errors"
	"strings"
	"testing"
)

// testParams are cheap on purpose: argon2id at the production cost takes tens of
// milliseconds per call, which multiplied across a table of malformed inputs makes
// the unit tier slow enough that people stop running it. The production parameters
// still get exercised, by TestDefaultParamsAreValid and by the bounds tests.
func testParams() PasswordParams {
	return PasswordParams{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

func newTestHasher(t *testing.T) *PasswordHasher {
	t.Helper()
	hasher, err := NewPasswordHasher(testParams())
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	return hasher
}

func TestHashAndVerify(t *testing.T) {
	hasher := newTestHasher(t)

	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	// The encoding is what verification depends on, so it is asserted rather than
	// assumed: a change to the format is a change to every stored credential.
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("encoded hash does not carry the expected parameters: %s", encoded)
	}
	if fields := strings.Split(encoded, "$"); len(fields) != 6 {
		t.Fatalf("encoded hash has %d fields, want 6: %s", len(fields), encoded)
	}

	verification, err := hasher.Verify("correct horse battery staple", encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verification.Matches {
		t.Error("the correct password did not verify")
	}
	if verification.NeedsRehash {
		t.Error("a hash produced with the current parameters reported NeedsRehash")
	}

	wrong, err := hasher.Verify("Correct horse battery staple", encoded)
	if err != nil {
		t.Fatalf("Verify with a wrong password: %v", err)
	}
	if wrong.Matches {
		t.Error("a wrong password verified")
	}
}

func TestHashIsSalted(t *testing.T) {
	hasher := newTestHasher(t)

	first, err := hasher.Hash("same password twice")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := hasher.Hash("same password twice")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	// Identical hashes would mean an unsalted or fixed-salt scheme, which makes
	// equal passwords detectable across accounts and precomputation shared.
	if first == second {
		t.Fatal("hashing the same password twice produced the same hash")
	}

	for _, encoded := range []string{first, second} {
		verification, err := hasher.Verify("same password twice", encoded)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !verification.Matches {
			t.Errorf("each independent hash should verify: %s", encoded)
		}
	}
}

func TestVerifyReportsNeedsRehash(t *testing.T) {
	// A credential stored under weaker parameters, as it would be after the cost
	// was raised.
	oldHasher, err := NewPasswordHasher(PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	encoded, err := oldHasher.Hash("a password worth upgrading")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	currentHasher, err := NewPasswordHasher(PasswordParams{
		Memory: 2048, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}

	verification, err := currentHasher.Verify("a password worth upgrading", encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verification.Matches {
		t.Error("a hash from older parameters should still verify")
	}
	if !verification.NeedsRehash {
		t.Error("a hash from older parameters should report NeedsRehash, which is how the " +
			"parameters get raised without a schema change")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	hasher := newTestHasher(t)

	valid, err := hasher.Hash("a password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	cases := map[string]string{
		"empty":                     "",
		"not a hash":                "hunter2",
		"wrong algorithm":           strings.Replace(valid, "$argon2id$", "$argon2i$", 1),
		"missing field":             "$argon2id$v=19$m=1024,t=1,p=1$c2FsdA",
		"wrong version":             strings.Replace(valid, "v=19", "v=16", 1),
		"parameters not name=value": strings.Replace(valid, "m=1024,t=1,p=1", "m=1024,1,p=1", 1),
		"parameter not a number":    strings.Replace(valid, "m=1024", "m=many", 1),
		"unknown parameter":         strings.Replace(valid, "m=1024", "x=1024", 1),
		"missing parameter":         strings.Replace(valid, ",p=1", "", 1),
		"empty parameter field":     strings.Replace(valid, "m=1024,t=1,p=1", "", 1),
		// Built explicitly rather than by substituting into `valid`: the salt and
		// key there are random and base64, so a pattern meant to corrupt them would
		// often fail to match, and the case would silently become a valid hash.
		"salt not base64": "$argon2id$v=19$m=1024,t=1,p=1$!!!!$" +
			"aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA",
		"key not base64": "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2E$!!!!",
		"empty salt": "$argon2id$v=19$m=1024,t=1,p=1$$" +
			"aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA",
		"empty key":        "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2E$",
		"trailing garbage": valid + "$extra",
	}

	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := hasher.Verify("a password", encoded); !errors.Is(err, ErrPasswordHashInvalid) {
				t.Errorf("expected ErrPasswordHashInvalid, got %v", err)
			}
		})
	}
}

func TestVerifyBoundsStoredParameters(t *testing.T) {
	hasher := newTestHasher(t)

	// Verification derives its cost from the stored string, so an absurd value must
	// be refused rather than attempted: otherwise a corrupt or hostile row turns a
	// login into an out-of-memory kill.
	cases := map[string]string{
		"memory":      "$argon2id$v=19$m=999999999,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA",
		"iterations":  "$argon2id$v=19$m=1024,t=9000,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA",
		"parallelism": "$argon2id$v=19$m=1024,t=1,p=255$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA",
	}

	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := hasher.Verify("a password", encoded); !errors.Is(err, ErrPasswordHashInvalid) {
				t.Errorf("expected ErrPasswordHashInvalid, got %v", err)
			}
		})
	}
}

func TestNewPasswordHasherRejectsInvalidParams(t *testing.T) {
	valid := testParams()

	cases := map[string]PasswordParams{
		"zero memory":     withMemory(valid, 0),
		"absurd memory":   withMemory(valid, maxHashMemory+1),
		"zero iterations": withIterations(valid, 0),
		"many iterations": withIterations(valid, maxHashIterations+1),
		"zero lanes":      withParallelism(valid, 0),
		"short salt":      withSaltLength(valid, 4),
		"short key":       withKeyLength(valid, 8),
	}

	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPasswordHasher(params); err == nil {
				t.Error("expected the parameters to be rejected")
			}
		})
	}
}

func TestDefaultParamsAreValid(t *testing.T) {
	// The production parameters are only ever exercised here, at the real cost, so
	// that a change to them cannot ship unverified.
	hasher, err := NewPasswordHasher(DefaultPasswordParams())
	if err != nil {
		t.Fatalf("the default parameters are invalid: %v", err)
	}

	encoded, err := hasher.Hash("a password hashed at production cost")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	verification, err := hasher.Verify("a password hashed at production cost", encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verification.Matches {
		t.Error("a password hashed with the default parameters did not verify")
	}
}

func TestVerifyUnknownAccountNeverMatches(t *testing.T) {
	hasher := newTestHasher(t)

	// The dummy hash has to be a well-formed hash capable of being verified: a
	// malformed one would return early with an error instead of spending the work,
	// which is the entire point of the method.
	if _, err := parsePasswordHash(hasher.DummyHash()); err != nil {
		t.Fatalf("the dummy hash is not parseable: %v", err)
	}

	for _, password := range []string{"", "password", hasher.DummyHash()} {
		verification, err := hasher.Verify(password, hasher.DummyHash())
		if err != nil {
			t.Fatalf("Verify against the dummy hash: %v", err)
		}
		if verification.Matches {
			t.Errorf("the dummy hash matched %q; an unregistered address would then be "+
				"indistinguishable from a registered one that happens to use it", password)
		}
	}
}

func withMemory(p PasswordParams, v uint32) PasswordParams     { p.Memory = v; return p }
func withIterations(p PasswordParams, v uint32) PasswordParams { p.Iterations = v; return p }
func withParallelism(p PasswordParams, v uint8) PasswordParams { p.Parallelism = v; return p }
func withSaltLength(p PasswordParams, v uint32) PasswordParams { p.SaltLength = v; return p }
func withKeyLength(p PasswordParams, v uint32) PasswordParams  { p.KeyLength = v; return p }

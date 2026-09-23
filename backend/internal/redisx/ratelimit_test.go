package redisx

import (
	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
)

// The counter's two commands are issued in one transaction and both go to Redis, so its
// behaviour is Redis's to provide and cannot be reproduced without one. What this file
// asserts is the part that is this package's to get wrong: that the type still satisfies the
// interface the middleware declares. A rename or a signature change therefore fails here,
// next to the code, rather than as a build error in an unrelated package.
//
// The round trip — counting, expiring and clearing — is covered by integration_test.go,
// which runs in CI against a real Redis (ADR-003).
var _ authmw.Counter = (*AttemptCounter)(nil)

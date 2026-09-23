// Package audit owns the shape of the audit trail.
//
// docs/16 requires audit to be kept separate from ordinary logging, and
// MASTER_PROMPT requires every high-risk administrative or financial action to be
// recorded. Those two requirements are why this is a package rather than a logging
// convention: an audit entry is a row with columns that can be filtered and
// retained independently of the log, not a line that shares the log's lifecycle.
//
// The package deliberately owns no action names. Each domain names its own actions
// — identity names its sign-ins, commerce will name its adjustments — so the trail's
// vocabulary grows with the domains that produce it instead of being enumerated in
// one file that every phase has to edit.
package audit

import (
	"context"

	"github.com/google/uuid"
)

// Actor types.
//
// They mirror the isolation docs/14 requires between users, admins and provider
// credentials, plus `system` for work the platform performs on its own behalf (the
// reconciler, the outbox relay). The set is closed: the audit table constrains it, so
// a new actor type is a migration rather than a typo.
const (
	ActorUser     = "user"
	ActorAdmin    = "admin"
	ActorSystem   = "system"
	ActorProvider = "provider"
)

// Context is where an action came from.
//
// Every field is optional. A background job has no request behind it, and requiring
// values it cannot supply would push callers into inventing them.
type Context struct {
	IP        string
	UserAgent string
	RequestID string
	TraceID   string
}

// Event is one entry in the trail.
type Event struct {
	ActorType string
	// ActorID is the acting subject. It may be nil for a system action.
	ActorID      uuid.UUID
	Action       string
	ResourceType string
	ResourceID   uuid.UUID
	// Details is recorded as the entry's after-data.
	//
	// The table also has a before-data column, which is unused in Phase 1: nothing it
	// audits yet has a previous state. It is left to the phases that adjust
	// something — a balance, a subscription — where the previous value is the
	// interesting part.
	Details map[string]string
	Context Context
}

// Recorder writes entries to the trail.
//
// It is an interface so a caller can be tested without a database, and so a later
// phase can wrap it — routing entries through the outbox, for instance — without
// touching the callers.
type Recorder interface {
	Record(ctx context.Context, event Event) error
}

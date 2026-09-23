package identitystore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
)

// Recorder writes audit entries.
//
// It is separate from Directory even though both write to the same database, because
// they are separate concerns: a phase that adjusts a balance will want the recorder
// without wanting the identity statements.
type Recorder struct{ queries *sqlcgen.Queries }

// NewRecorder returns a recorder over a connection or a transaction.
//
// Accepting sqlcgen.DBTX is what makes "write the audit entry in the same transaction
// as the change it describes" possible: docs/16 requires the entry to survive exactly
// when the change does, and a recorder bound to a pool could not honour that.
func NewRecorder(db sqlcgen.DBTX) *Recorder { return &Recorder{queries: sqlcgen.New(db)} }

var _ audit.Recorder = (*Recorder)(nil)

// Record writes one entry.
func (r *Recorder) Record(ctx context.Context, event audit.Event) error {
	// Details are stored as JSON so the trail can carry fields this phase never
	// anticipated without a migration per field. An empty map is written as `{}`
	// rather than NULL: a reader then always has an object to query.
	details := event.Details
	if details == nil {
		details = map[string]string{}
	}
	after, err := json.Marshal(details)
	if err != nil {
		// Only reachable if a caller put something unmarshalable into Details, which
		// is a programming error rather than an operational one, so it is reported
		// rather than swallowed.
		return fmt.Errorf("identitystore: encode audit details: %w", err)
	}

	if err := r.queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{
		ID:           uuid.New(),
		ActorType:    event.ActorType,
		ActorID:      uuidOrNil(event.ActorID),
		Action:       event.Action,
		ResourceType: event.ResourceType,
		ResourceID:   uuidOrNil(event.ResourceID),
		// before_data is left NULL. Nothing audited in Phase 1 changes an existing
		// value; the phases that do will set it, and the column exists for them.
		BeforeData: nil,
		AfterData:  after,
		IpAddress:  parseIP(event.Context.IP),
		UserAgent:  textOrNull(event.Context.UserAgent),
		RequestID:  textOrNull(event.Context.RequestID),
		TraceID:    textOrNull(event.Context.TraceID),
	}); err != nil {
		return fmt.Errorf("identitystore: insert audit entry: %w", err)
	}

	return nil
}

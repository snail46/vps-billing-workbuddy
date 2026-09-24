// Package outbox delivers the transactional outbox (docs/09, ADR-005).
//
// The write side commits an event beside the change it describes; this half is
// the worker's: claim what is due, hand it to a registered handler, and record
// the delivery. The database is the queue — SKIP LOCKED claims share the batch
// among workers, and a failed delivery retries on the backoff the operations
// use. Consumers deduplicate by event id, so a redelivered event is waste, not
// corruption.
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
)

// Event is one claimed delivery.
type Event struct {
	ID            uuid.UUID
	EventType     string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       []byte
	Attempts      int
}

// Handler delivers one event type. Returning an error schedules the retry.
type Handler func(ctx context.Context, event Event) error

// Publisher claims and delivers.
type Publisher struct {
	queries  *sqlcgen.Queries
	handlers map[string]Handler
	logger   *slog.Logger
	now      func() time.Time
}

// New builds a publisher over the given queries.
func New(queries *sqlcgen.Queries, logger *slog.Logger) *Publisher {
	return &Publisher{
		queries:  queries,
		handlers: map[string]Handler{},
		logger:   logger,
		now:      time.Now,
	}
}

// Register mounts a handler for an event type. A duplicate is a startup error.
func (p *Publisher) Register(eventType string, handler Handler) error {
	if _, exists := p.handlers[eventType]; exists {
		return fmt.Errorf("outbox: a handler is already registered for %s", eventType)
	}
	p.handlers[eventType] = handler
	return nil
}

// Deliver claims one batch of due events for the types it can handle and
// delivers each. It returns the number delivered. Events whose type has no
// handler are left pending — dropping them would be a silent failure wearing a
// scheduler's clothes.
func (p *Publisher) Deliver(ctx context.Context) (int, error) {
	if len(p.handlers) == 0 {
		return 0, nil
	}
	types := make([]string, 0, len(p.handlers))
	for eventType := range p.handlers {
		types = append(types, eventType)
	}

	at := p.now().UTC()
	rows, err := p.queries.ClaimDueOutboxEvents(ctx, sqlcgen.ClaimDueOutboxEventsParams{
		NextAttemptAt: pgtype.Timestamptz{Time: at, Valid: true},
		Column2:       types,
	})
	if err != nil {
		return 0, fmt.Errorf("outbox: claim: %w", err)
	}

	delivered := 0
	for i := range rows {
		row := rows[i]
		event := Event{
			ID:            row.ID,
			EventType:     row.EventType,
			AggregateType: row.AggregateType,
			AggregateID:   row.AggregateID,
			Payload:       row.Payload,
			Attempts:      int(row.Attempts),
		}
		if err := p.deliver(ctx, event, at); err != nil {
			p.logger.ErrorContext(ctx, "an outbox delivery failed and will retry",
				slog.String("event_id", event.ID.String()),
				slog.String("event_type", event.EventType),
				slog.String("error", err.Error()))
			continue
		}
		delivered++
	}
	return delivered, nil
}

func (p *Publisher) deliver(ctx context.Context, event Event, at time.Time) error {
	handler := p.handlers[event.EventType]
	if err := handler(ctx, event); err != nil {
		next := at.Add(operation.BackoffFor(event.Attempts))
		if _, markErr := p.queries.MarkOutboxFailed(ctx, sqlcgen.MarkOutboxFailedParams{
			ID:            event.ID,
			NextAttemptAt: pgtype.Timestamptz{Time: next, Valid: true},
		}); markErr != nil {
			return fmt.Errorf("outbox: mark the retry: %w (handler: %w)", markErr, err)
		}
		return err
	}
	if _, err := p.queries.MarkOutboxPublished(ctx, sqlcgen.MarkOutboxPublishedParams{
		ID:          event.ID,
		PublishedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("outbox: mark the delivery: %w", err)
	}
	return nil
}

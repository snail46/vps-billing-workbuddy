package middleware

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
)

// Correlation headers.
const (
	// HeaderRequestID carries a per-request identifier back to the client so a
	// user-visible failure can be quoted in a support ticket.
	HeaderRequestID = "X-Request-ID"
	// HeaderTraceID carries the cross-process trace identifier.
	HeaderTraceID = "X-Trace-ID"
	// headerTraceparent is the W3C Trace Context header.
	headerTraceparent = "Traceparent"
)

// maxCorrelationIDLen bounds a client-supplied correlation identifier.
const maxCorrelationIDLen = 128

// RequestID ensures every request carries a request identifier.
//
// A client-supplied X-Request-ID is accepted only if it is structurally safe:
// it is echoed into a response header and into every structured log record for
// the request, so an unchecked value would allow log forging via control
// characters. Unsafe or absent values are replaced with a fresh UUIDv7, which
// docs/04 mandates for core identifiers.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validCorrelationID(id) {
			id = newCorrelationID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// TraceID ensures every request carries a trace identifier.
//
// When the caller supplies a valid W3C `traceparent`, its trace-id is adopted so
// that a request already traced upstream continues the same trace; otherwise a
// fresh identifier is generated. Trace continuity across
// request -> operation -> workflow -> provider task is required by docs/16.
func TraceID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := traceIDFromTraceparent(r.Header.Get(headerTraceparent))
		if id == "" {
			id = newCorrelationID()
		}
		w.Header().Set(HeaderTraceID, id)
		next.ServeHTTP(w, r.WithContext(logging.WithTraceID(r.Context(), id)))
	})
}

// newCorrelationID returns a UUIDv7 string.
//
// UUIDv7 is time-ordered, so identifiers sort chronologically in logs and
// indexes — the same property docs/04 relies on for core entity identifiers.
// A generation failure falls back to UUIDv4 rather than failing the request:
// correlation is diagnostic, and losing a request over it would be worse than
// losing its ordering.
func newCorrelationID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// validCorrelationID reports whether a client-supplied identifier is safe to
// echo into a header and into structured logs.
func validCorrelationID(s string) bool {
	if s == "" || len(s) > maxCorrelationIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':', r == '/', r == '+', r == '=', r == '@':
		default:
			// Rejects control characters (log injection), spaces and any
			// non-ASCII content.
			return false
		}
	}
	return true
}

// traceIDFromTraceparent extracts the trace-id from a W3C traceparent header.
//
// Format: version(2 hex) "-" trace-id(32 hex) "-" parent-id(16 hex) "-" flags(2 hex).
// An unparseable or all-zero trace-id yields "" so the caller generates one.
func traceIDFromTraceparent(traceparent string) string {
	traceparent = strings.TrimSpace(traceparent)
	if traceparent == "" {
		return ""
	}

	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 {
		return ""
	}
	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]

	if len(version) != 2 || len(traceID) != 32 || len(parentID) != 16 || len(flags) != 2 {
		return ""
	}
	if !isHex(version) || !isHex(traceID) || !isHex(parentID) || !isHex(flags) {
		return ""
	}
	// The spec reserves version "ff" and forbids an all-zero trace-id.
	if strings.EqualFold(version, "ff") || traceID == strings.Repeat("0", 32) {
		return ""
	}
	return strings.ToLower(traceID)
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// Package logging builds the process logger and guarantees that correlation
// identifiers travel with every log record.
//
// docs/16-OBSERVABILITY.md requires JSON structured logging carrying
// request_id / trace_id / actor / action / resource / operation_id / provider /
// node. The first three of those are cross-cutting, so rather than trusting
// every call site to remember them, ContextHandler injects the identifiers that
// are present on the record's context automatically.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Supported output formats.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// New builds the process logger writing to w. A nil writer means os.Stdout.
//
// An unknown level or format is reported as an error rather than silently
// falling back: a production process that quietly logs at the wrong level or in
// the wrong format is worse than one that refuses to start.
func New(level, format string, w io.Writer) (*slog.Logger, error) {
	if w == nil {
		w = os.Stdout
	}

	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var base slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatJSON:
		base = slog.NewJSONHandler(w, opts)
	case FormatText:
		base = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("logging: unsupported format %q (want %q or %q)", format, FormatJSON, FormatText)
	}

	return slog.New(ContextHandler{inner: base}), nil
}

// ParseLevel maps a configuration string onto a slog level.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: unsupported level %q (want debug, info, warn or error)", level)
	}
}

// ContextHandler decorates records with the correlation identifiers found on the
// context passed to the handler.
type ContextHandler struct {
	inner slog.Handler
}

// Enabled reports whether the handler handles records at the given level.
func (h ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle annotates the record with request_id / trace_id / operation_id when
// those values are present on ctx, then delegates to the wrapped handler.
func (h ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	// record is a value copy owned by this call, so mutating it is safe.
	if id := RequestIDFrom(ctx); id != "" {
		record.AddAttrs(slog.String("request_id", id))
	}
	if id := TraceIDFrom(ctx); id != "" {
		record.AddAttrs(slog.String("trace_id", id))
	}
	if id := OperationIDFrom(ctx); id != "" {
		record.AddAttrs(slog.String("operation_id", id))
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs returns a handler with the given attributes pre-attached.
func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup returns a handler with the given group name.
func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{inner: h.inner.WithGroup(name)}
}

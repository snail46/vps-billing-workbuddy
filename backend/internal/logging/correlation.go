package logging

import (
	"context"
)

// Correlation identifiers are carried on the context so that any log record
// produced anywhere in a request or operation lifetime is automatically
// annotated. The keys are unexported so that only this package can write them;
// readers use the exported accessors below.

type ctxKey int

const (
	requestIDKey ctxKey = iota
	traceIDKey
	operationIDKey
)

// WithRequestID returns a context carrying the given request identifier.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// WithTraceID returns a context carrying the given trace identifier.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// WithOperationID returns a context carrying the given operation identifier.
//
// docs/16 requires trace continuity request -> operation -> workflow -> provider
// task; the operation identifier is the link between a request and the
// asynchronous work it spawned.
func WithOperationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, operationIDKey, id)
}

// RequestIDFrom returns the request identifier, or "" when absent.
func RequestIDFrom(ctx context.Context) string { return stringFrom(ctx, requestIDKey) }

// TraceIDFrom returns the trace identifier, or "" when absent.
func TraceIDFrom(ctx context.Context) string { return stringFrom(ctx, traceIDKey) }

// OperationIDFrom returns the operation identifier, or "" when absent.
func OperationIDFrom(ctx context.Context) string { return stringFrom(ctx, operationIDKey) }

func stringFrom(ctx context.Context, key ctxKey) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(key).(string)
	if !ok {
		return ""
	}
	return v
}

// Package middleware provides the cross-cutting HTTP concerns shared by both
// API surfaces.
//
// Ordering matters and is asserted in the router: correlation identifiers must
// be established before anything can log, the response writer must be wrapped
// before any handler (or panic recovery) writes, and panic recovery must sit
// inside the access log so a recovered panic is still recorded as a 500.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type responseWriterKey struct{}

// ResponseWriterFrom returns the wrapped response writer installed by
// Observability. It lets panic recovery determine whether a response has
// already been committed.
func ResponseWriterFrom(ctx context.Context) (chimw.WrapResponseWriter, bool) {
	ww, ok := ctx.Value(responseWriterKey{}).(chimw.WrapResponseWriter)
	return ww, ok
}

// Observability wraps the response writer and emits one structured access log
// record per request.
//
// It is the only place a request is logged, so the record always carries the
// correlation identifiers (injected by the ContextHandler), the resolved route
// pattern rather than the raw path, and the real response status.
func Observability(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			ctx := context.WithValue(r.Context(), responseWriterKey{}, ww)
			start := time.Now()

			defer func() {
				duration := time.Since(start)
				status := ww.Status()

				logger.LogAttrs(ctx, levelForStatus(status), "http request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("route", routePattern(ctx)),
					slog.Int("status", status),
					slog.Int64("duration_ms", duration.Milliseconds()),
					slog.Int("bytes", ww.BytesWritten()),
					slog.String("remote_ip", ClientIP(r)),
					slog.String("user_agent", r.UserAgent()),
				)
			}()

			next.ServeHTTP(ww, r.WithContext(ctx))
		})
	}
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// ClientIP returns the address of the direct peer.
//
// Exported because two things must agree on it: the address recorded in the access
// log and the address a rate limit counts against. If those disagreed, an operator
// reading the log could not explain a limit that fired.
//
// Forwarded-IP headers are intentionally not consulted: nothing in the Phase 0
// topology sits in front of the server, and trusting those headers without knowing
// the proxy topology would let a client forge the address recorded here and counted
// here. See the note in httpapi.NewRouter.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// routePattern returns the matched chi route pattern rather than the raw path.
//
// Logging the pattern keeps cardinality bounded: /api/v1/admin/orders/{id} is
// one series, whereas the raw path would be one series per order id.
func routePattern(ctx context.Context) string {
	rctx := chi.RouteContext(ctx)
	if rctx == nil {
		return ""
	}
	return rctx.RoutePattern()
}

package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
)

// Timeout bounds how long a request may occupy a handler.
//
// The standard library has no equivalent and chi's middleware.Timeout is not
// suitable here for two reasons: it writes a bare 504 with no body, which breaks
// the response envelope in docs/08, and it writes that status even when the
// handler has already committed a response, producing a superfluous header
// write. This implementation keeps the envelope authoritative and only acts when
// nothing has been sent yet.
//
// The deadline is set on the context, so handlers that respect ctx.Done() (as
// every database and provider call must) return promptly. A handler that ignores
// its context still gets the envelope, but the underlying work is not forcibly
// killed — Go cannot do that safely. Cancellation is therefore cooperative by
// design, which is why every I/O call must take the request context.
func Timeout(timeout time.Duration, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))

			if ctx.Err() != context.DeadlineExceeded {
				return
			}

			// The response may already be committed; in that case the client has
			// a complete response and writing another status would be invalid.
			if ww, ok := ResponseWriterFrom(r.Context()); ok && ww.BytesWritten() > 0 {
				logger.WarnContext(r.Context(), "request exceeded deadline after response was committed",
					slog.Duration("timeout", timeout))
				return
			}

			httpx.WriteError(w, r, logger, httpx.ErrRequestTimeout().WithDetails(map[string]any{
				"timeout_ms": timeout.Milliseconds(),
			}))
		})
	}
}

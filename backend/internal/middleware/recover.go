package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
)

// Recover converts a panic into a 500 that still satisfies the response
// envelope, and records the panic with its stack.
//
// docs/AGENTS.md forbids silent failure, so a panic must never simply terminate
// the connection: it is logged with correlation identifiers and the client
// receives a contract-shaped error whose request_id points at the log record.
//
// Two cases are handled specially:
//
//   - http.ErrAbortHandler is re-panicked. net/http uses it to abort a response
//     deliberately, and swallowing it would change that behaviour.
//   - If the response was already partially written, no envelope can be sent;
//     the panic is logged and the connection is left to net/http.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// errors.Is rather than == so a wrapped abort still propagates.
				if err, isErr := recovered.(error); isErr && errors.Is(err, http.ErrAbortHandler) {
					panic(recovered)
				}

				logger.ErrorContext(r.Context(), "panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
				)

				if ww, ok := ResponseWriterFrom(r.Context()); ok && ww.BytesWritten() > 0 {
					return
				}

				httpx.WriteError(w, r, logger, httpx.ErrInternal())
			}()

			next.ServeHTTP(w, r)
		})
	}
}

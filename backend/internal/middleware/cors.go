package middleware

import (
	"net/http"
	"strings"
)

// CORS headers. The method and header lists are fixed rather than reflected from
// the request: reflecting `Access-Control-Request-Headers` would let a caller
// widen the allowed header set, and the platform's own surface is known.
const (
	corsAllowMethods  = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders  = "Accept, Content-Type, Authorization, Idempotency-Key, X-Request-ID, X-CSRF-Token"
	corsExposeHeaders = "X-Request-ID, X-Trace-ID"
	corsMaxAgeSeconds = "600"
)

// CORS applies an origin allowlist.
//
// docs/14-SECURITY.md requires a production allowlist rather than a wildcard,
// and credentials are permitted only for allowlisted origins. A disallowed
// origin receives no CORS headers, which is what makes the browser block the
// response; it is deliberately not converted into a 403, because a same-origin
// or non-browser caller is legitimate and the browser is the component enforcing
// the policy.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The body does not vary by Origin but the CORS headers do, so a
			// shared cache must key on it.
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			_, originAllowed := allowed[origin]

			if origin != "" && originAllowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Expose-Headers", corsExposeHeaders)
			}

			if isPreflight(r) {
				if originAllowed {
					w.Header().Add("Vary", "Access-Control-Request-Method")
					w.Header().Add("Vary", "Access-Control-Request-Headers")
					w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
					w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
					w.Header().Set("Access-Control-Max-Age", corsMaxAgeSeconds)
				}
				// Preflight is answered here and never reaches a handler, which
				// would otherwise reject OPTIONS.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

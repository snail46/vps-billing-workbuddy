package middleware

import "net/http"

// SecurityHeadersOptions configures SecurityHeaders.
type SecurityHeadersOptions struct {
	// EnableHSTS adds Strict-Transport-Security.
	//
	// It is only meaningful over TLS: sending HSTS on a plaintext origin has no
	// effect, and pinning HSTS during local HTTP development would make the
	// browser refuse plaintext for the host afterwards. Callers therefore enable
	// it only in production.
	EnableHSTS bool
}

// SecurityHeaders sets defensive response headers on every API response.
//
// A Content-Security-Policy is intentionally absent: this process serves JSON
// only, and no HTML is rendered from it, so a CSP would be cargo-culted. The
// header belongs on the web frontends that serve documents, and is applied by
// their own server configuration.
func SecurityHeaders(opts SecurityHeadersOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			if opts.EnableHSTS {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

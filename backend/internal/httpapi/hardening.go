// Release hardening's HTTP surface (ADR-015): the admin's second-factor
// enrollment and the metrics scrape.
package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// --------------------------------------------------------------- admin 2FA

type totpCodeRequest struct {
	Code string `json:"code"`
}

type totpSetupResponse struct {
	Secret string `json:"secret"`
	URL    string `json:"url"`
}

// adminTwoFactorSetup mints a secret for the caller. The flag does not move
// until `enable` verifies one code — an enrollment that locks the admin out
// of their own account would be the setup failing twice.
func (a *api) adminTwoFactorSetup(w http.ResponseWriter, r *http.Request) {
	adminID, ok := a.adminPrincipal(w, r)
	if !ok {
		return
	}
	secret, err := identity.GenerateTOTPSecret()
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	// The mint writes disabled: the account keeps working until a code proves
	// the secret is in the admin's authenticator.
	if err := a.admin.Store.SetAdminTwoFactor(r.Context(), adminID, secret, false, time.Now().UTC()); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	email, err := a.admin.Store.Queries().AdminTwoFactorEmail(r.Context(), adminID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, totpSetupResponse{
		Secret: secret,
		URL:    identity.OTPAuthURL(email, secret),
	})
}

// adminTwoFactorEnable verifies one code against the minted secret and flips
// the flag. From here, sign-in demands the code.
func (a *api) adminTwoFactorEnable(w http.ResponseWriter, r *http.Request) {
	a.adminTwoFactorFlip(w, r, true)
}

// adminTwoFactorDisable verifies one code and clears the factor.
func (a *api) adminTwoFactorDisable(w http.ResponseWriter, r *http.Request) {
	a.adminTwoFactorFlip(w, r, false)
}

func (a *api) adminTwoFactorFlip(w http.ResponseWriter, r *http.Request, enabled bool) {
	adminID, ok := a.adminPrincipal(w, r)
	if !ok {
		return
	}
	var body totpCodeRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	row, err := a.admin.Store.Queries().AdminTwoFactorByAdmin(r.Context(), adminID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	if !row.TwoFactorEnabled && !enabled && !row.TwoFactorSecret.Valid {
		// Disabling a factor that was never set is a no-op success.
		httpx.WriteData(w, r, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if !identity.VerifyTOTP(row.TwoFactorSecret.String, body.Code, time.Now().UTC()) {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrForbidden(), "errors.invalid_credentials"))
		return
	}
	secret := row.TwoFactorSecret.String
	if !enabled {
		secret = ""
	}
	if err := a.admin.Store.SetAdminTwoFactor(r.Context(), adminID, secret, enabled, time.Now().UTC()); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"enabled": enabled})
}

// adminPrincipal reads the admin principal behind the session.
func (a *api) adminPrincipal(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok || principal.Session.Subject != identity.SubjectAdmin {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return uuid.Nil, false
	}
	return principal.Session.SubjectID, true
}

// ---------------------------------------------------------------- metrics

// adminMetrics renders the Prometheus text exposition (ADR-015 §2): live
// queries, computed on scrape, readable with curl.
func (a *api) adminMetrics(w http.ResponseWriter, r *http.Request) {
	ops, err := a.admin.Store.Queries().MetricsOperationsByStatus(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	instances, err := a.admin.Store.Queries().MetricsInstancesByState(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)

	write := func(name, help, labels string, value int64) {
		_, _ = w.Write([]byte("# HELP " + name + " " + help + "\n# TYPE " + name + " gauge\n"))
		_, _ = w.Write([]byte(name + "{" + labels + "} " + strconv.FormatInt(value, 10) + "\n"))
	}
	write("vps_operations", "Operations by status.", "", 0)
	for i := range ops {
		row := &ops[i]
		_, _ = w.Write([]byte("vps_operations{status=\"" + row.Status + "\"} " + strconv.FormatInt(row.Count, 10) + "\n"))
	}
	write("vps_instances", "Instances by observed state.", "", 0)
	for i := range instances {
		row := &instances[i]
		_, _ = w.Write([]byte("vps_instances{state=\"" + row.ObservedState + "\"} " + strconv.FormatInt(row.Count, 10) + "\n"))
	}
	pool := a.admin.Store.Pool().Stat()
	_, _ = w.Write([]byte("# HELP vps_db_pool_acquired Connections currently acquired.\n# TYPE vps_db_pool_acquired gauge\n"))
	_, _ = w.Write([]byte("vps_db_pool_acquired " + strconv.FormatInt(int64(pool.AcquiredConns()), 10) + "\n"))
}

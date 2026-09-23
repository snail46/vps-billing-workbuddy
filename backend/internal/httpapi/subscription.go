package httpapi

// The subscription surface.
//
// The customer reads their subscriptions and asks for cancellation; the machine's
// calendar is the sweep's, not theirs, so there is no endpoint that mutates
// periods directly. Renew-now exists because the customer can see a due bill and
// should not have to wait for the next sweep pass to pay it — and it reaches the
// same transaction the sweep does, so there is one renewal, wherever it starts.

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
)

// subscriptionPayload is a subscription as the API exposes it.
type subscriptionPayload struct {
	ID           string     `json:"id"`
	PlanID       string     `json:"plan_id"`
	Status       string     `json:"status"`
	BillingCycle string     `json:"billing_cycle"`
	PriceMinor   int64      `json:"price_minor"`
	Currency     string     `json:"currency"`
	StartedAt    *time.Time `json:"started_at"`
	// CurrentPeriod is the span of service that has been paid for. The customer
	// reads the end of it: that is when the next bill is due.
	CurrentPeriodStart time.Time  `json:"current_period_start"`
	CurrentPeriodEnd   time.Time  `json:"current_period_end"`
	NextDueAt          time.Time  `json:"next_due_at"`
	GraceUntil         *time.Time `json:"grace_until"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	EndedAt            *time.Time `json:"ended_at"`
	Version            int64      `json:"version"`
}

func newSubscriptionPayload(s commerce.Subscription) subscriptionPayload {
	return subscriptionPayload{
		ID:                 s.ID.String(),
		PlanID:             s.PlanID.String(),
		Status:             s.Status,
		BillingCycle:       s.BillingCycle,
		PriceMinor:         s.Price.AmountMinor,
		Currency:           string(s.Price.Currency),
		StartedAt:          s.StartedAt,
		CurrentPeriodStart: s.CurrentPeriodStart,
		CurrentPeriodEnd:   s.CurrentPeriodEnd,
		NextDueAt:          s.NextDueAt,
		GraceUntil:         s.GraceUntil,
		CancelAtPeriodEnd:  s.CancelAtPeriodEnd,
		EndedAt:            s.EndedAt,
		Version:            s.Version,
	}
}

// subscriptionFailure extends the commerce mapping with the subscription's own
// failures. It is a function beside commerceFailure rather than a case in it,
// because the two surfaces share the commerce package but not its vocabulary.
func subscriptionFailure(err error) *httpx.APIError {
	switch {
	case errors.Is(err, commerce.ErrSubscriptionNotFound):
		return withMessageKey(httpx.ErrNotFound(), "errors.not_found")
	case errors.Is(err, commerce.ErrSubscriptionNotLive):
		return withMessageKey(httpx.ErrConflict(), "errors.subscription_not_live")
	case errors.Is(err, commerce.ErrNothingDue):
		return withMessageKey(httpx.ErrConflict(), "errors.nothing_due")
	case errors.Is(err, commerce.ErrInsufficientBalance):
		return withMessageKey(httpx.ErrConflict(), "errors.insufficient_balance")
	default:
		return commerceFailure(err)
	}
}

// listSubscriptions returns the signed-in customer's subscriptions.
func (a *api) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}
	subscriptions, err := a.commerce.Service.ListSubscriptionsForUser(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, subscriptionFailure(err))
		return
	}
	payload := make([]subscriptionPayload, 0, len(subscriptions))
	for i := range subscriptions {
		payload = append(payload, newSubscriptionPayload(subscriptions[i]))
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"subscriptions": payload})
}

// getSubscription returns one of the signed-in customer's subscriptions. The
// owner is part of the lookup: someone else's subscription is none.
func (a *api) getSubscription(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}
	subscriptionID, err := uuid.Parse(chi.URLParam(r, "subscriptionID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	subscription, err := a.commerce.Service.GetSubscription(r.Context(),
		principal.Session.SubjectID, subscriptionID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, subscriptionFailure(err))
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"subscription": newSubscriptionPayload(subscription)})
}

// cancelSubscription records the customer's request to end the subscription when
// the paid-for period runs out. The period itself is theirs; the machine honours
// the request when it would next bill.
func (a *api) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}
	subscriptionID, err := uuid.Parse(chi.URLParam(r, "subscriptionID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	if err := a.commerce.Service.RequestCancellation(r.Context(),
		principal.Session.SubjectID, subscriptionID, time.Now()); err != nil {
		httpx.WriteError(w, r, a.logger, subscriptionFailure(err))
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"cancel_at_period_end": true})
}

// renewSubscription pays a due renewal immediately, from the wallet.
//
// The sweep would reach it on its own pass; this is the customer declining to
// wait. It answers with the subscription in its new period, or with the reason
// nothing happened — a bill that is not due yet, or a balance that cannot cover
// the one that is.
func (a *api) renewSubscription(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}
	subscriptionID, err := uuid.Parse(chi.URLParam(r, "subscriptionID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	subscription, err := a.commerce.Service.RenewNow(r.Context(),
		principal.Session.SubjectID, subscriptionID, time.Now())
	if err != nil {
		httpx.WriteError(w, r, a.logger, subscriptionFailure(err))
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"subscription": newSubscriptionPayload(subscription)})
}

// terminateSubscription ends a subscription on an administrator's authority. It
// is the first permission-gated endpoint the administrative surface mounts, and
// it registers through the guarded path of adminRoutes for exactly that reason.
func (a *api) terminateSubscription(w http.ResponseWriter, r *http.Request) {
	subscriptionID, err := uuid.Parse(chi.URLParam(r, "subscriptionID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	if err := a.commerce.Service.Terminate(r.Context(), subscriptionID, time.Now()); err != nil {
		httpx.WriteError(w, r, a.logger, subscriptionFailure(err))
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"status": commerce.SubscriptionTerminated})
}

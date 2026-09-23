package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/money"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment"
)

// The commerce surface. The catalogue is public — it is a storefront, and a customer
// who cannot see the price list cannot be expected to buy anything — while everything
// that spends or commits money is behind a session, a CSRF token, or a gateway
// signature, in that order of strength.

// CommerceDeps are the collaborators the commerce surface needs.
type CommerceDeps struct {
	Service  *commerce.Service
	Gateways commerce.Gateways
}

// productPayload is a catalogue entry as the API exposes it.
type productPayload struct {
	ID    string            `json:"id"`
	Slug  string            `json:"slug"`
	Name  map[string]string `json:"name"`
	Plans []planPayload     `json:"plans"`
}

type planPayload struct {
	ID             string            `json:"id"`
	Slug           string            `json:"slug"`
	Name           map[string]string `json:"name"`
	CpuCores       string            `json:"cpu_cores"`
	MemoryMB       int               `json:"memory_mb"`
	DiskGB         int               `json:"disk_gb"`
	Virtualization string            `json:"virtualization"`
	BillingCycle   string            `json:"billing_cycle"`
	// PriceMinor is the amount in the currency's minor unit. The exponent is the
	// viewer's business, not the server's (docs/13), so no currency symbol or decimal
	// point is applied here.
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"`
}

type orderItemPayload struct {
	PlanID         string            `json:"plan_id"`
	Slug           string            `json:"slug"`
	Name           map[string]string `json:"name"`
	Quantity       int               `json:"quantity"`
	UnitPriceMinor int64             `json:"unit_price_minor"`
	TotalMinor     int64             `json:"total_minor"`
	Currency       string            `json:"currency"`
}

type orderPayload struct {
	ID            string             `json:"id"`
	OrderNo       string             `json:"order_no"`
	Status        string             `json:"status"`
	SubtotalMinor int64              `json:"subtotal_minor"`
	DiscountMinor int64              `json:"discount_minor"`
	TotalMinor    int64              `json:"total_minor"`
	Currency      string             `json:"currency"`
	Items         []orderItemPayload `json:"items"`
	PaidAt        *time.Time         `json:"paid_at"`
	CreatedAt     time.Time          `json:"created_at"`
}

type paymentPayload struct {
	PaymentNo        string     `json:"payment_no"`
	OrderID          string     `json:"order_id"`
	Gateway          string     `json:"gateway"`
	Status           string     `json:"status"`
	AmountMinor      int64      `json:"amount_minor"`
	Currency         string     `json:"currency"`
	GatewayPaymentID string     `json:"gateway_payment_id"`
	PayURL           string     `json:"pay_url"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// createOrderRequest is the body of POST /orders.
type createOrderRequest struct {
	Items []struct {
		PlanID   string `json:"plan_id"`
		Quantity int    `json:"quantity"`
	} `json:"items"`
	// DiscountMinor is optional and defaults to zero. It is accepted from the client
	// because a promotion is chosen on the customer's side; the total it produces is
	// still computed here, and the schema refuses one that exceeds the subtotal.
	DiscountMinor int64 `json:"discount_minor"`
}

// startPaymentRequest is the body of POST /orders/{id}/payments.
type startPaymentRequest struct {
	Gateway string `json:"gateway"`
}

// listCatalog returns the products that are on sale, with their plans.
func (a *api) listCatalog(w http.ResponseWriter, r *http.Request) {
	products, err := a.commerce.Service.ListActiveProducts(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}
	plans, err := a.commerce.Service.ListActivePlans(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	byProduct := make(map[uuid.UUID][]planPayload, len(products))
	for i := range plans {
		plan := plans[i]
		byProduct[plan.ProductID] = append(byProduct[plan.ProductID], planPayload{
			ID:             plan.ID.String(),
			Slug:           plan.Slug,
			Name:           plan.Name,
			CpuCores:       plan.CpuCores,
			MemoryMB:       plan.MemoryMB,
			DiskGB:         plan.DiskGB,
			Virtualization: plan.Virtualization,
			BillingCycle:   plan.BillingCycle,
			PriceMinor:     plan.Price.AmountMinor,
			Currency:       string(plan.Price.Currency),
		})
	}

	payload := make([]productPayload, 0, len(products))
	for i := range products {
		product := products[i]
		// Encoded as [] rather than null, so a client rendering the catalogue never has
		// to treat "no plans" and "not returned" differently.
		plans := byProduct[product.ID]
		if plans == nil {
			plans = []planPayload{}
		}
		payload = append(payload, productPayload{
			ID:    product.ID.String(),
			Slug:  product.Slug,
			Name:  product.Name,
			Plans: plans,
		})
	}

	httpx.WriteData(w, r, http.StatusOK, map[string]any{"products": payload})
}

// createOrder prices and records an order for the signed-in customer.
func (a *api) createOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	var body createOrderRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	if len(body.Items) == 0 {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.empty_order"))
		return
	}

	requested := make([]commerce.OrderLineRequest, 0, len(body.Items))
	for _, item := range body.Items {
		planID, err := uuid.Parse(item.PlanID)
		if err != nil {
			// The identifier is validated here rather than reported as "no such plan",
			// because the two mean different things: one is a malformed request and the
			// other is a plan that has been withdrawn.
			httpx.WriteError(w, r, a.logger, invalidField("items.plan_id"))
			return
		}
		requested = append(requested, commerce.OrderLineRequest{PlanID: planID, Quantity: item.Quantity})
	}

	discount, err := money.New(body.DiscountMinor, "CNY")
	if err != nil {
		httpx.WriteError(w, r, a.logger, invalidField("discount_minor"))
		return
	}

	order, err := a.commerce.Service.CreateOrder(r.Context(), principal.Session.SubjectID, requested, discount)
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	httpx.WriteData(w, r, http.StatusCreated, map[string]any{"order": newOrderPayload(order)})
}

// listOrders returns the signed-in customer's orders.
func (a *api) listOrders(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	orders, err := a.commerce.Service.ListOrders(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	payload := make([]orderPayload, 0, len(orders))
	for i := range orders {
		payload = append(payload, newOrderPayload(orders[i]))
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"orders": payload})
}

// getOrder returns one of the signed-in customer's orders.
func (a *api) getOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	orderID, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}

	order, err := a.commerce.Service.GetOrder(r.Context(), principal.Session.SubjectID, orderID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"order": newOrderPayload(order)})
}

// startPayment asks a gateway to take an order's money.
func (a *api) startPayment(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	orderID, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}

	var body startPaymentRequest
	if !a.decodeBody(w, r, &body) {
		return
	}

	started, intent, err := a.commerce.Service.StartPayment(r.Context(), principal.Session.SubjectID, orderID, body.Gateway)
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	httpx.WriteData(w, r, http.StatusCreated, map[string]any{"payment": paymentPayload{
		PaymentNo:        started.PaymentNo,
		OrderID:          started.OrderID.String(),
		Gateway:          started.Gateway,
		Status:           started.Status,
		AmountMinor:      started.Amount.AmountMinor,
		Currency:         string(started.Amount.Currency),
		GatewayPaymentID: intent.GatewayPaymentID,
		PayURL:           intent.PayURL,
		ExpiresAt:        intent.ExpiresAt,
	}})
}

// paymentWebhook applies a gateway callback.
//
// It is public, because a gateway cannot hold a session. What authenticates it is the
// signature over the raw body, which is checked before anything is looked up — so a
// forged callback costs an attacker exactly the verification and nothing else.
func (a *api) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	gatewayName := chi.URLParam(r, "gateway")
	gateway, err := a.commerce.Gateways.Gateway(gatewayName)
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	// The raw bytes are what the signature was computed over, so they are read as bytes
	// and passed as bytes. Decoding first and re-encoding would verify a different body
	// than the one that arrived.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxRequestBodyBytes))
	if err != nil {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrBadRequest(), "errors.malformed_callback"))
		return
	}

	result, err := a.commerce.Service.Settle(r.Context(), gatewayName, body, r.Header.Get(gateway.SignatureHeader()))
	if err != nil {
		httpx.WriteError(w, r, a.logger, commerceFailure(err))
		return
	}

	// 200 either way. The gateway's obligation was to notify; whether this was the
	// delivery that did the work or the ninety-ninth that repeated it, it has been met,
	// and answering anything else would tell the gateway to keep trying.
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"settled":         !result.AlreadySettled,
		"already_settled": result.AlreadySettled,
	})
}

// commerceFailure maps a domain failure onto the transport contract.
//
// It is written once, like every other mapping, because it is the whole of what a
// caller learns. The distinctions that matter: a missing order is not the same as an
// order that cannot be paid (the first means "check the identifier", the second means
// "check the state"), and a mismatched callback amount is a conflict rather than a
// replay, because the two sources disagree and someone has to be told.
func commerceFailure(err error) *httpx.APIError {
	switch {
	case errors.Is(err, commerce.ErrOrderNotFound),
		errors.Is(err, commerce.ErrPaymentNotFound),
		errors.Is(err, commerce.ErrPlanNotFound),
		errors.Is(err, commerce.ErrUnknownGateway):
		return withMessageKey(httpx.ErrNotFound(), "errors.not_found")
	case errors.Is(err, commerce.ErrOrderNotPayable):
		return withMessageKey(httpx.ErrConflict(), "errors.order_not_payable")
	case errors.Is(err, commerce.ErrPaymentAlreadyStarted):
		return withMessageKey(httpx.ErrConflict(), "errors.payment_already_started")
	case errors.Is(err, commerce.ErrAmountMismatch):
		return withMessageKey(httpx.ErrConflict(), "errors.payment_amount_mismatch")
	case errors.Is(err, commerce.ErrCurrencyMismatch):
		return withMessageKey(httpx.ErrConflict(), "errors.payment_currency_mismatch")
	case errors.Is(err, commerce.ErrInvalidQuantity):
		return invalidField("items.quantity")
	case errors.Is(err, commerce.ErrInvalidAmount):
		return invalidField("amount")
	case errors.Is(err, commerce.ErrInvalidDecimal):
		return withMessageKey(httpx.ErrValidation(), "errors.invalid_plan")
	case errors.Is(err, commerce.ErrMixedCurrencies):
		return withMessageKey(httpx.ErrValidation(), "errors.mixed_currencies")
	case errors.Is(err, commerce.ErrEmptyOrder):
		return withMessageKey(httpx.ErrValidation(), "errors.empty_order")
	case errors.Is(err, commerce.ErrPlanNotPurchaseable):
		return withMessageKey(httpx.ErrValidation(), "errors.plan_not_purchaseable")
	case errors.Is(err, commerce.ErrUnsupportedNotification):
		return withMessageKey(httpx.ErrValidation(), "errors.unsupported_notification")
	case errors.Is(err, payment.ErrSignatureInvalid):
		return withMessageKey(httpx.ErrUnauthorized(), "errors.signature_invalid")
	case errors.Is(err, payment.ErrMalformedCallback):
		return withMessageKey(httpx.ErrBadRequest(), "errors.malformed_callback")
	default:
		return httpx.ErrInternal().WithCause(err)
	}
}

func newOrderPayload(order commerce.Order) orderPayload {
	items := make([]orderItemPayload, 0, len(order.Items))
	for i := range order.Items {
		item := order.Items[i]
		total, err := item.Total()
		if err != nil {
			// Unreachable for a line the domain built; a payload that omitted the line
			// would be a worse answer than a panic, because the order exists and the
			// client has to be told about all of it.
			panic(fmt.Sprintf("httpapi: price an order line: %v", err))
		}
		items = append(items, orderItemPayload{
			PlanID:         item.PlanID.String(),
			Slug:           item.PlanSnapshot.Slug,
			Name:           item.PlanSnapshot.Name,
			Quantity:       item.Quantity,
			UnitPriceMinor: item.UnitPrice.AmountMinor,
			TotalMinor:     total.AmountMinor,
			Currency:       string(item.UnitPrice.Currency),
		})
	}
	if items == nil {
		items = []orderItemPayload{}
	}

	return orderPayload{
		ID:            order.ID.String(),
		OrderNo:       order.OrderNo,
		Status:        order.Status,
		SubtotalMinor: order.Subtotal.AmountMinor,
		DiscountMinor: order.Discount.AmountMinor,
		TotalMinor:    order.Total.AmountMinor,
		Currency:      string(order.Total.Currency),
		Items:         items,
		PaidAt:        order.PaidAt,
		CreatedAt:     order.CreatedAt,
	}
}

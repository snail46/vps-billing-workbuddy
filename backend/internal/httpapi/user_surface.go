// The customer's own surface (ADR-011): instance detail and actions, the
// operation read and event stream, wallet, invoices, notifications and
// tickets. Every handler here is session-scoped; every read reaches the
// caller's rows through the ownership join, never through a second check.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/instanceop"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/storage/usersurface"
)

// UserDeps are the collaborators the customer's own surface needs.
type UserDeps struct {
	Store      *usersurface.Store
	Operations *operationstore.Store
	Instances  *instancestore.Store
}

// imageShape is what a reinstall may name: no colon (the idempotency key's
// image segment is colon-free by construction), no emptiness, no whitespace.
var imageShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// --------------------------------------------------------------- instances

type instanceDetailPayload struct {
	instancePayload
	Networks     []instanceNetworkPayload     `json:"networks"`
	PortForwards []instancePortForwardPayload `json:"port_forwards"`
	Traffic      []instanceTrafficPayload     `json:"traffic"`
	// OpenOperation is the live workflow on this machine, if any. The detail
	// page is how a refreshed browser recovers the progress a stream was
	// showing: the row is the state, and this is where it is found.
	OpenOperation *operationPayload `json:"open_operation"`
}

type instanceNetworkPayload struct {
	Type              string    `json:"type"`
	Address           *string   `json:"address"`
	Gateway           *string   `json:"gateway"`
	Prefix            *int32    `json:"prefix"`
	ProviderNetworkID *string   `json:"provider_network_id"`
	CreatedAt         time.Time `json:"created_at"`
}

type instancePortForwardPayload struct {
	Protocol          string    `json:"protocol"`
	PublicIP          string    `json:"public_ip"`
	PublicPort        int32     `json:"public_port"`
	GuestPort         int32     `json:"guest_port"`
	Description       *string   `json:"description"`
	Status            string    `json:"status"`
	ProviderMappingID *string   `json:"provider_mapping_id"`
	CreatedAt         time.Time `json:"created_at"`
}

type instanceTrafficPayload struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	RXBytes     int64     `json:"rx_bytes"`
	TXBytes     int64     `json:"tx_bytes"`
	Source      string    `json:"source"`
}

// getInstance answers with one of the caller's instances and everything the
// detail page reads beside it.
func (a *api) getInstance(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	instanceID, ok := a.urlUUID(w, r, "instanceID")
	if !ok {
		return
	}

	instance, owned, err := a.user.Instances.ByID(r.Context(), instanceID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	// The instance's ownership rides on the same join the store's ForUser
	// uses; a row that is not the caller's is a row that does not exist.
	if !owned || !a.ownsInstance(r, instance, principal.Session.SubjectID) {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	detail, err := a.user.Store.Detail(r.Context(), instanceID, principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	// The machine's live workflow, if one is running: the operation read and
	// the event stream both hang off this identifier.
	var openOperation *operationPayload
	if live, exists, err := a.user.Operations.OpenByResource(r.Context(), "instance", instanceID); err == nil && exists {
		payload := newOperationPayload(live)
		openOperation = &payload
	}

	payload := instanceDetailPayload{
		instancePayload: newInstancePayload(instance),
		Networks:        make([]instanceNetworkPayload, 0, len(detail.Networks)),
		PortForwards:    make([]instancePortForwardPayload, 0, len(detail.PortForwards)),
		Traffic:         make([]instanceTrafficPayload, 0, len(detail.Traffic)),
		OpenOperation:   openOperation,
	}
	for i := range detail.Networks {
		n := &detail.Networks[i]
		payload.Networks = append(payload.Networks, instanceNetworkPayload{
			Type: n.Type, Address: n.Address, Gateway: n.Gateway, Prefix: n.Prefix,
			ProviderNetworkID: n.ProviderNetworkID, CreatedAt: n.CreatedAt,
		})
	}
	for i := range detail.PortForwards {
		f := &detail.PortForwards[i]
		payload.PortForwards = append(payload.PortForwards, instancePortForwardPayload{
			Protocol: f.Protocol, PublicIP: f.PublicIP, PublicPort: f.PublicPort,
			GuestPort: f.GuestPort, Description: f.Description, Status: f.Status,
			ProviderMappingID: f.ProviderMappingID, CreatedAt: f.CreatedAt,
		})
	}
	for i := range detail.Traffic {
		t := &detail.Traffic[i]
		payload.Traffic = append(payload.Traffic, instanceTrafficPayload{
			PeriodStart: t.PeriodStart, PeriodEnd: t.PeriodEnd,
			RXBytes: t.RXBytes, TXBytes: t.TXBytes, Source: t.Source,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, payload)
}

// restartInstance accepts the caller's restart request as an operation.
func (a *api) restartInstance(w http.ResponseWriter, r *http.Request) {
	a.acceptInstanceAction(w, r, func(ctx context.Context, instanceID uuid.UUID) (operation.Operation, error) {
		return instanceop.EnqueueRestartOnStore(ctx, a.user.Operations, instanceID, time.Now().UTC())
	})
}

type reinstallRequest struct {
	ImageID string `json:"image_id"`
}

// reinstallInstance accepts the caller's reinstall request as an operation.
func (a *api) reinstallInstance(w http.ResponseWriter, r *http.Request) {
	var body reinstallRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	if !imageShape.MatchString(body.ImageID) {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.invalid_image"))
		return
	}
	a.acceptInstanceAction(w, r, func(ctx context.Context, instanceID uuid.UUID) (operation.Operation, error) {
		return instanceop.EnqueueReinstallOnStore(ctx, a.user.Operations, instanceID, body.ImageID, time.Now().UTC())
	})
}

// acceptInstanceAction is the shape both actions share: the caller owns a
// running instance, nothing else is already working on it, and the answer is
// the operation the worker will execute.
func (a *api) acceptInstanceAction(w http.ResponseWriter, r *http.Request,
	enqueue func(ctx context.Context, instanceID uuid.UUID) (operation.Operation, error)) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	instanceID, ok := a.urlUUID(w, r, "instanceID")
	if !ok {
		return
	}

	instance, owned, err := a.user.Instances.ByID(r.Context(), instanceID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	if !owned || !a.ownsInstance(r, instance, principal.Session.SubjectID) {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	if instance.ObservedState != instancestore.ObservedRunning {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrConflict(), "errors.instance_not_running"))
		return
	}
	if _, live, err := a.user.Operations.OpenByResource(r.Context(), "instance", instanceID); err == nil && live {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrConflict(), "errors.operation_in_progress"))
		return
	}

	accepted, err := enqueue(r.Context(), instanceID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusAccepted, map[string]any{
		"operation_id": accepted.ID.String(),
		"status":       accepted.Status,
	})
}

// ownsInstance resolves the ownership join for one instance row.
func (a *api) ownsInstance(r *http.Request, instance instancestore.Instance, userID uuid.UUID) bool {
	owner, err := a.user.Store.InstanceOwner(r.Context(), instance.ID)
	if err != nil {
		return false
	}
	return owner == userID
}

// --------------------------------------------------------------- operations

// getUserOperation answers with the caller's own operation and its steps.
func (a *api) getUserOperation(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	operationID, ok := a.urlUUID(w, r, "operationID")
	if !ok {
		return
	}
	op, err := a.user.Operations.ByID(r.Context(), operationID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	if !a.operationOwnedBy(r, op, principal.Session.SubjectID) {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	steps, err := a.user.Operations.Steps(r.Context(), operationID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, operationDetailPayload{
		Operation: newOperationPayload(op),
		Steps:     newOperationSteps(steps),
	})
}

// operationOwnedBy resolves the operation's resource to its owner. An unknown
// resource type is never owned: the operation types are the two this platform
// runs, and anything else is not the caller's to see.
func (a *api) operationOwnedBy(r *http.Request, op operation.Operation, userID uuid.UUID) bool {
	switch op.ResourceType {
	case "subscription":
		owner, err := a.user.Store.SubscriptionOwner(r.Context(), op.ResourceID)
		return err == nil && owner == userID
	case "instance":
		owner, err := a.user.Store.InstanceOwner(r.Context(), op.ResourceID)
		return err == nil && owner == userID
	default:
		return false
	}
}

// userStreamEvents is the caller's own operation stream — the same machine
// the operator watches, behind the ownership gate (ADR-011 §3).
func (a *api) userStreamEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	operationID, err := uuid.Parse(r.URL.Query().Get("operation_id"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrValidation())
		return
	}
	op, err := a.user.Operations.ByID(r.Context(), operationID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	if !a.operationOwnedBy(r, op, principal.Session.SubjectID) {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	a.streamOperation(w, r, operationID)
}

// ------------------------------------------------------------------- wallet

// getWallet answers with the caller's balances and the movements behind them.
func (a *api) getWallet(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	wallets, err := a.user.Store.Wallets(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	movements, err := a.user.Store.Ledger(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	walletPayloads := make([]map[string]any, 0, len(wallets))
	for i := range wallets {
		w := &wallets[i]
		walletPayloads = append(walletPayloads, map[string]any{
			"id":                      w.ID.String(),
			"currency":                w.Currency,
			"available_balance_minor": w.AvailableBalanceMinor,
		})
	}
	ledgerPayloads := make([]map[string]any, 0, len(movements))
	for i := range movements {
		m := &movements[i]
		entry := map[string]any{
			"transaction_type": m.TransactionType,
			"direction":        m.Direction,
			"amount_minor":     m.AmountMinor,
			"currency":         m.Currency,
			"created_at":       m.CreatedAt,
		}
		if m.ReferenceType != nil {
			entry["reference_type"] = *m.ReferenceType
		}
		if m.ReferenceID != nil {
			entry["reference_id"] = m.ReferenceID.String()
		}
		if m.Description != nil {
			entry["description"] = *m.Description
		}
		ledgerPayloads = append(ledgerPayloads, entry)
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"wallets": walletPayloads,
		"ledger":  ledgerPayloads,
	})
}

// ----------------------------------------------------------------- invoices

// listInvoices answers with the caller's invoices.
func (a *api) listInvoices(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	invoices, err := a.user.Store.Invoices(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payloads := make([]map[string]any, 0, len(invoices))
	for i := range invoices {
		payloads = append(payloads, invoiceMap(&invoices[i]))
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"invoices": payloads})
}

// getInvoice answers with one of the caller's invoices and its lines.
func (a *api) getInvoice(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	invoiceID, ok := a.urlUUID(w, r, "invoiceID")
	if !ok {
		return
	}
	invoice, err := a.user.Store.Invoice(r.Context(), invoiceID, principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	items, err := a.user.Store.InvoiceItems(r.Context(), invoice.ID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	itemPayloads := make([]map[string]any, 0, len(items))
	for i := range items {
		item := &items[i]
		var description any
		_ = json.Unmarshal(item.DescriptionI18n, &description)
		itemPayloads = append(itemPayloads, map[string]any{
			"id":                item.ID.String(),
			"description_i18n":  description,
			"quantity":          item.Quantity,
			"unit_amount_minor": item.UnitAmountMinor,
			"total_minor":       item.TotalMinor,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"invoice": invoiceMap(invoice),
		"items":   itemPayloads,
	})
}

func invoiceMap(invoice *usersurface.Invoice) map[string]any {
	entry := map[string]any{
		"id":           invoice.ID.String(),
		"invoice_no":   invoice.InvoiceNo,
		"status":       invoice.Status,
		"amount_minor": invoice.AmountMinor,
		"currency":     invoice.Currency,
		"created_at":   invoice.CreatedAt,
	}
	if invoice.SubscriptionID != nil {
		entry["subscription_id"] = invoice.SubscriptionID.String()
	}
	if invoice.OrderID != nil {
		entry["order_id"] = invoice.OrderID.String()
	}
	if invoice.DueAt != nil {
		entry["due_at"] = *invoice.DueAt
	}
	if invoice.PaidAt != nil {
		entry["paid_at"] = *invoice.PaidAt
	}
	return entry
}

// ------------------------------------------------------------ notifications

// listNotifications answers with the caller's notifications and the unread
// count the navigation badge reads.
func (a *api) listNotifications(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	notifications, err := a.user.Store.Notifications(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	unread, err := a.user.Store.UnreadCount(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payloads := make([]map[string]any, 0, len(notifications))
	for i := range notifications {
		n := &notifications[i]
		var parameters any
		_ = json.Unmarshal(n.Parameters, &parameters)
		entry := map[string]any{
			"id":          n.ID.String(),
			"type":        n.Type,
			"title_key":   n.TitleKey,
			"message_key": n.MessageKey,
			"parameters":  parameters,
			"severity":    n.Severity,
			"created_at":  n.CreatedAt,
		}
		if n.ReadAt != nil {
			entry["read_at"] = *n.ReadAt
		}
		payloads = append(payloads, entry)
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"notifications": payloads,
		"unread":        unread,
	})
}

// markNotificationRead stamps one of the caller's notifications as read.
func (a *api) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	notificationID, ok := a.urlUUID(w, r, "notificationID")
	if !ok {
		return
	}
	if _, err := a.user.Store.MarkRead(r.Context(), notificationID, principal.Session.SubjectID, time.Now().UTC()); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"read": true})
}

// ------------------------------------------------------------------ tickets

type createTicketRequest struct {
	Subject  string `json:"subject"`
	Priority string `json:"priority"`
	Message  string `json:"message"`
}

// listTickets answers with the caller's conversations.
func (a *api) listTickets(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	tickets, err := a.user.Store.Tickets(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payloads := make([]map[string]any, 0, len(tickets))
	for i := range tickets {
		payloads = append(payloads, ticketMap(&tickets[i]))
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"tickets": payloads})
}

// createTicket opens a conversation with its first message.
func (a *api) createTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	var body createTicketRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	if body.Subject == "" || body.Message == "" {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.ticket_incomplete"))
		return
	}
	switch body.Priority {
	case "":
		body.Priority = usersurface.PriorityNormal
	case usersurface.PriorityLow, usersurface.PriorityNormal, usersurface.PriorityHigh:
	default:
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.invalid_priority"))
		return
	}
	now := time.Now().UTC()
	ticketID := uuid.New()
	ticketNo, err := usersurface.NewTicketNumber(now)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	if err := a.user.Store.TicketOpen(r.Context(), ticketID, ticketNo, principal.Session.SubjectID,
		body.Subject, body.Priority, body.Message, now); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusCreated, map[string]any{
		"id":        ticketID.String(),
		"ticket_no": ticketNo,
		"status":    "open",
	})
}

// getTicket answers with one of the caller's conversations and its turns.
func (a *api) getTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	ticketID, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	ticket, err := a.user.Store.Ticket(r.Context(), ticketID, principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	messages, err := a.user.Store.TicketMessages(r.Context(), ticket.ID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	messagePayloads := make([]map[string]any, 0, len(messages))
	for i := range messages {
		m := &messages[i]
		entry := map[string]any{
			"id":          m.ID.String(),
			"sender_type": m.SenderType,
			"message":     m.Message,
			"created_at":  m.CreatedAt,
		}
		if m.SenderID != nil {
			entry["sender_id"] = m.SenderID.String()
		}
		messagePayloads = append(messagePayloads, entry)
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"ticket":   ticketMap(ticket),
		"messages": messagePayloads,
	})
}

type ticketMessageRequest struct {
	Message string `json:"message"`
}

// addTicketMessage appends the caller's turn to their own conversation.
func (a *api) addTicketMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	ticketID, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	var body ticketMessageRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	if body.Message == "" {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.empty_message"))
		return
	}
	// The ownership join first: a message to a foreign ticket is a message to
	// no ticket.
	if _, err := a.user.Store.Ticket(r.Context(), ticketID, principal.Session.SubjectID); err != nil {
		httpx.WriteError(w, r, a.logger, userFailure(err))
		return
	}
	if err := a.user.Store.TicketReply(r.Context(), ticketID, principal.Session.SubjectID, body.Message, time.Now().UTC()); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusCreated, map[string]any{"appended": true})
}

// closeTicket closes one of the caller's conversations.
func (a *api) closeTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.userPrincipal(w, r)
	if !ok {
		return
	}
	ticketID, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	closed, err := a.user.Store.TicketClose(r.Context(), ticketID, principal.Session.SubjectID, time.Now().UTC())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	if !closed {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"closed": true})
}

func ticketMap(ticket *usersurface.Ticket) map[string]any {
	entry := map[string]any{
		"id":         ticket.ID.String(),
		"ticket_no":  ticket.TicketNo,
		"subject":    ticket.Subject,
		"status":     ticket.Status,
		"priority":   ticket.Priority,
		"created_at": ticket.CreatedAt,
		"updated_at": ticket.UpdatedAt,
	}
	if ticket.ClosedAt != nil {
		entry["closed_at"] = *ticket.ClosedAt
	}
	return entry
}

// ----------------------------------------------------------------- helpers

// userPrincipal reads the session's user principal, answering 401 itself so
// the handlers read as their business logic alone.
func (a *api) userPrincipal(w http.ResponseWriter, r *http.Request) (authmw.Principal, bool) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok || principal.Session.Subject != identity.SubjectUser {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return authmw.Principal{}, false
	}
	return principal, true
}

// urlUUID parses a path parameter, answering 404 for a malformed one — a
// malformed identifier names nothing, and nothing is not a validation error
// the caller can fix.
func (a *api) urlUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return uuid.Nil, false
	}
	return id, true
}

// userFailure maps the store's misses to their one answer: a 404 that cannot
// distinguish "does not exist" from "is not yours".
func userFailure(err error) error {
	if errors.Is(err, usersurface.ErrNotFound) {
		return withMessageKey(httpx.ErrNotFound(), "errors.not_found")
	}
	return httpx.ErrInternal()
}

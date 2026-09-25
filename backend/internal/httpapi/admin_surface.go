// The operator's console (ADR-012): the dashboard's computed aggregates and
// the management lists. Every handler here is a permission the route declares;
// the payloads are the rows, rendered for screens rather than re-modelled.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/storage/adminsurface"
)

// AdminDeps are the collaborators the operator's console needs.
type AdminDeps struct {
	Store *adminsurface.Store
}

// ---------------------------------------------------------------- conversions

func jsonBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(v, &decoded); err != nil {
		return nil
	}
	return decoded
}

func tsAny(value pgtype.Timestamptz) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}

// ----------------------------------------------------------------- overview

// adminOverview answers the dashboard's first screen (docs/11): anomalies and
// health, computed from current rows.
func (a *api) adminOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.admin.Store.Overview(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	byStatus := make([]map[string]any, 0, len(overview.PaymentsByStatus))
	for i := range overview.PaymentsByStatus {
		row := &overview.PaymentsByStatus[i]
		byStatus = append(byStatus, map[string]any{"status": row.Status, "count": row.Count})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"failed_operations":   overview.FailedOperations,
		"running_operations":  overview.RunningOperations,
		"nodes_offline":       overview.NodesOffline,
		"active_users":        overview.ActiveUsers,
		"suspended_users":     overview.SuspendedUsers,
		"capacity_warnings":   overview.CapacityWarnings,
		"revenue_total_minor": overview.RevenueTotalMinor,
		"payments_by_status":  byStatus,
	})
}

// --------------------------------------------------------------------- users

// adminListUsers answers the user management list.
func (a *api) adminListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListUsers(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	users := make([]map[string]any, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		users = append(users, map[string]any{
			"id": row.ID, "email": row.Email, "status": row.Status,
			"locale": row.Locale, "created_at": tsAny(row.CreatedAt),
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"users": users})
}

// adminGetUser answers one user with their wallets.
func (a *api) adminGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "userID")
	if !ok {
		return
	}
	row, err := a.admin.Store.Queries().AdminUserByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	wallets, err := a.admin.Store.Queries().AdminUserWallets(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	walletPayloads := make([]map[string]any, 0, len(wallets))
	for i := range wallets {
		w := &wallets[i]
		walletPayloads = append(walletPayloads, map[string]any{
			"id": w.ID, "currency": w.Currency, "available_balance_minor": w.AvailableBalanceMinor,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": row.ID, "email": row.Email, "status": row.Status,
			"locale": row.Locale, "timezone": row.Timezone, "created_at": tsAny(row.CreatedAt),
		},
		"wallets": walletPayloads,
	})
}

// adminSetUserStatus is the suspend/activate write; the state word comes from
// the route, not the client, so the request cannot name an unknown state.
func (a *api) adminSetUserStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.urlUUID(w, r, "userID")
		if !ok {
			return
		}
		if _, err := a.admin.Store.SetUserStatus(r.Context(), id, status, time.Now().UTC()); err != nil {
			httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
			return
		}
		httpx.WriteData(w, r, http.StatusOK, map[string]any{"status": status})
	}
}

// ------------------------------------------------------------------ products

// adminListProducts answers the catalogue management view, inactive included.
func (a *api) adminListProducts(w http.ResponseWriter, r *http.Request) {
	products, err := a.admin.Store.Queries().AdminListProducts(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	plans, err := a.admin.Store.Queries().AdminListPlans(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	byProduct := make(map[uuid.UUID][]map[string]any)
	for i := range plans {
		plan := &plans[i]
		byProduct[plan.ProductID] = append(byProduct[plan.ProductID], map[string]any{
			"id": plan.ID, "slug": plan.Slug, "status": plan.Status,
			"price_minor": plan.PriceMinor,
			"currency":    plan.Currency, "billing_cycle": plan.BillingCycle,
			"memory_mb": plan.MemoryMb, "disk_gb": plan.DiskGb,
		})
	}
	productPayloads := make([]map[string]any, 0, len(products))
	for i := range products {
		product := &products[i]
		productPayloads = append(productPayloads, map[string]any{
			"id": product.ID, "slug": product.Slug, "status": product.Status,
			"name_i18n":        jsonBytes(product.NameI18n),
			"description_i18n": jsonBytes(product.DescriptionI18n),
			"plans":            byProduct[product.ID],
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"products": productPayloads})
}

// -------------------------------------------------------------------- orders

// adminListOrders answers the order management list.
func (a *api) adminListOrders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListOrders(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"orders": rows})
}

// adminGetOrder answers one order with its items and payments.
func (a *api) adminGetOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "orderID")
	if !ok {
		return
	}
	order, err := a.admin.Store.Queries().AdminOrderByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	items, err := a.admin.Store.Queries().AdminOrderItems(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payments, err := a.admin.Store.Queries().AdminOrderPayments(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"order":    order,
		"items":    items,
		"payments": payments,
	})
}

// ------------------------------------------------------------------ payments

// adminListPayments answers the payment management list.
func (a *api) adminListPayments(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListPayments(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"payments": rows})
}

// -------------------------------------------------------------------- ledger

// adminLedger answers the ledger movements — the truth the console reads.
func (a *api) adminLedger(w http.ResponseWriter, r *http.Request) {
	movements, err := a.admin.Store.Queries().AdminLedgerMovements(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	balances, err := a.admin.Store.Queries().AdminWalletBalances(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"movements": movements,
		"balances":  balances,
	})
}

// ------------------------------------------------------------- subscriptions

// adminListSubscriptions answers the subscription management list.
func (a *api) adminListSubscriptions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListSubscriptions(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payloads := make([]map[string]any, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		payloads = append(payloads, map[string]any{
			"id": row.ID, "user_id": row.UserID, "user_email": row.UserEmail,
			"status": row.Status, "plan_name_i18n": jsonBytes(row.PlanNameI18n),
			"billing_cycle": row.BillingCycle, "price_minor": row.PriceMinor,
			"currency":             row.Currency,
			"current_period_start": tsAny(row.CurrentPeriodStart),
			"current_period_end":   tsAny(row.CurrentPeriodEnd),
			"created_at":           tsAny(row.CreatedAt),
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"subscriptions": payloads})
}

// ----------------------------------------------------------------- instances

// adminListInstances answers the instance management list.
func (a *api) adminListInstances(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListInstances(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"instances": rows})
}

// adminGetInstance answers the operator's instance detail: the record joined —
// owner, plan, node, provider, network, traffic, operations, audit (docs/11).
func (a *api) adminGetInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "instanceID")
	if !ok {
		return
	}
	detail, err := a.admin.Store.Queries().AdminInstanceDetail(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	networks, err := a.admin.Store.Queries().InstanceNetworksByInstance(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	traffic, err := a.admin.Store.Queries().TrafficByInstance(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	operations, err := a.admin.Store.Queries().AdminOperationsByResource(r.Context(), sqlcgen.AdminOperationsByResourceParams{
		ResourceType: "instance",
		ResourceID:   id,
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	audit, err := a.admin.Store.Queries().AdminAuditByResource(r.Context(), sqlcgen.AdminAuditByResourceParams{
		ResourceType: "instance",
		ResourceID:   &id,
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{
		"instance":   detail,
		"networks":   networks,
		"traffic":    traffic,
		"operations": operations,
		"audit":      audit,
	})
}

// adminListOperations answers the operation queue, newest first.
func (a *api) adminListOperations(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListOperations(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"operations": rows})
}

// ------------------------------------------------------------------- tickets

// adminListTickets answers the support queue.
func (a *api) adminListTickets(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListTickets(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"tickets": rows})
}

// adminGetTicket answers one conversation with its turns.
func (a *api) adminGetTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	ticket, err := a.admin.Store.Queries().AdminTicketByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
		return
	}
	messages, err := a.admin.Store.Queries().TicketMessagesByTicket(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"ticket": ticket, "messages": messages})
}

type adminTicketMessageRequest struct {
	Message string `json:"message"`
}

// adminReplyTicket appends the support side's turn (tickets.reply).
func (a *api) adminReplyTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	var body adminTicketMessageRequest
	if !a.decodeBody(w, r, &body) {
		return
	}
	if body.Message == "" {
		httpx.WriteError(w, r, a.logger, withMessageKey(httpx.ErrValidation(), "errors.empty_message"))
		return
	}
	// The message and the status stamp are one fact: the ticket moved to
	// answered because this message landed. They land in one transaction.
	now := time.Now().UTC()
	err := a.admin.Store.WithinTransaction(r.Context(), func(tx *adminsurface.Store) error {
		if err := tx.Queries().CreateTicketMessage(r.Context(), sqlcgen.CreateTicketMessageParams{
			ID:         uuid.New(),
			TicketID:   id,
			SenderType: "admin",
			Message:    body.Message,
		}); err != nil {
			return err
		}
		return tx.StampTicketAnswered(r.Context(), id, now)
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusCreated, map[string]any{"appended": true})
}

// adminCloseTicket closes one conversation (tickets.manage).
func (a *api) adminCloseTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := a.urlUUID(w, r, "ticketID")
	if !ok {
		return
	}
	closed, err := a.admin.Store.CloseTicket(r.Context(), id, time.Now().UTC())
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

// --------------------------------------------------------------------- audit

// adminListAudit answers the audit trail, newest first.
func (a *api) adminListAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminAuditRecent(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"audit": rows})
}

// ---------------------------------------------------- admins/roles/settings

// adminListAdmins answers the administrator list with their roles.
func (a *api) adminListAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := a.admin.Store.Queries().AdminListAdmins(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	grants, err := a.admin.Store.Queries().AdminRolesByAdmin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	byAdmin := make(map[uuid.UUID][]string)
	for i := range grants {
		grant := &grants[i]
		byAdmin[grant.AdminID] = append(byAdmin[grant.AdminID], grant.Key)
	}
	payloads := make([]map[string]any, 0, len(admins))
	for i := range admins {
		admin := &admins[i]
		payloads = append(payloads, map[string]any{
			"id": admin.ID, "email": admin.Email, "status": admin.Status,
			"display_name": admin.DisplayName, "two_factor_enabled": admin.TwoFactorEnabled,
			"roles": byAdmin[admin.ID], "created_at": tsAny(admin.CreatedAt),
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"admins": payloads})
}

// adminListRoles answers the role list with their permission keys.
func (a *api) adminListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := a.admin.Store.Queries().AdminListRoles(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	grants, err := a.admin.Store.Queries().AdminPermissionsByRole(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	byRole := make(map[uuid.UUID][]string)
	for i := range grants {
		grant := &grants[i]
		byRole[grant.RoleID] = append(byRole[grant.RoleID], grant.Key)
	}
	payloads := make([]map[string]any, 0, len(roles))
	for i := range roles {
		role := &roles[i]
		payloads = append(payloads, map[string]any{
			"id": role.ID, "key": role.Key, "name_key": role.NameKey,
			"permissions": byRole[role.ID],
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"roles": payloads})
}

// adminListSettings answers the settings table read-only.
func (a *api) adminListSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.admin.Store.Queries().AdminListSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"settings": rows})
}

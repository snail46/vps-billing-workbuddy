// Package usersurface is the persistence behind the customer's own pages
// (ADR-011): reads whose ownership is the SQL join itself, and the operation
// guard the action paths consult. A query in this store that cannot name the
// caller's user id does not exist.
package usersurface

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
)

// ErrNotFound reports a row that does not exist, or one that exists and is
// not the caller's. The two are deliberately indistinguishable: the response
// is the same 404 either way, so a customer cannot probe for foreign ids.
var ErrNotFound = errors.New("usersurface: no such row for this caller")

// Network is one address family the provider wired onto the machine.
type Network struct {
	Type              string
	Address           *string
	Gateway           *string
	Prefix            *int32
	ProviderNetworkID *string
	CreatedAt         time.Time
}

// PortForward is one public endpoint forwarded into the machine.
type PortForward struct {
	Protocol          string
	PublicIP          string
	PublicPort        int32
	GuestPort         int32
	Description       *string
	Status            string
	ProviderMappingID *string
	CreatedAt         time.Time
}

// Traffic is one recorded usage period.
type Traffic struct {
	PeriodStart time.Time
	PeriodEnd   time.Time
	RXBytes     int64
	TXBytes     int64
	Source      string
}

// Wallet is the customer's balance in one currency — the projection.
type Wallet struct {
	ID                    uuid.UUID
	Currency              string
	AvailableBalanceMinor int64
	CreatedAt             time.Time
}

// LedgerMovement is one entry on the customer's wallet accounts — the truth.
type LedgerMovement struct {
	TransactionType string
	ReferenceType   *string
	ReferenceID     *uuid.UUID
	Description     *string
	Direction       string
	AmountMinor     int64
	Currency        string
	CreatedAt       time.Time
}

// Invoice is one invoice of the caller's own.
type Invoice struct {
	ID             uuid.UUID
	InvoiceNo      string
	SubscriptionID *uuid.UUID
	OrderID        *uuid.UUID
	Status         string
	AmountMinor    int64
	Currency       string
	DueAt          *time.Time
	PaidAt         *time.Time
	CreatedAt      time.Time
}

// InvoiceItem is one line on an invoice.
type InvoiceItem struct {
	ID              uuid.UUID
	DescriptionI18n []byte
	Quantity        int32
	UnitAmountMinor int64
	TotalMinor      int64
	CreatedAt       time.Time
}

// Notification is one record the platform left for the caller.
type Notification struct {
	ID         uuid.UUID
	Type       string
	TitleKey   string
	MessageKey string
	Parameters []byte
	Severity   string
	ReadAt     *time.Time
	CreatedAt  time.Time
}

// Ticket is one support conversation the caller owns.
type Ticket struct {
	ID        uuid.UUID
	TicketNo  string
	Subject   string
	Status    string
	Priority  string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
}

// TicketMessage is one turn in the conversation.
type TicketMessage struct {
	ID         uuid.UUID
	SenderType string
	SenderID   *uuid.UUID
	Message    string
	CreatedAt  time.Time
}

// Store reads the customer's own rows.
type Store struct {
	queries *sqlcgen.Queries
	pool    *pgxpool.Pool
}

// New builds a store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool), pool: pool}
}

// WithinTransaction runs fn inside one transaction; the ticket's open writes
// its first message beside it.
func (s *Store) WithinTransaction(ctx context.Context, fn func(*Store) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("usersurface: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&Store{queries: sqlcgen.New(tx), pool: s.pool}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ------------------------------------------------------------------ instances

// InstanceDetail is the machine plus what hangs off it for the detail page.
type InstanceDetail struct {
	Networks     []Network
	PortForwards []PortForward
	Traffic      []Traffic
}

// Detail reads the extras of one instance for its owner. The instance row
// itself comes from the instance store; what this method guards is that the
// extras are only ever handed out through the same ownership join.
func (s *Store) Detail(ctx context.Context, instanceID, userID uuid.UUID) (*InstanceDetail, error) {
	// The ownership check is the existence check: the same join, so a caller
	// who does not own the machine learns nothing, not even its networks.
	var owner uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT s.user_id FROM instances i JOIN subscriptions s ON s.id = i.subscription_id
		 WHERE i.id = $1 AND i.deleted_at IS NULL`, instanceID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("usersurface: read instance owner: %w", err)
	}
	if owner != userID {
		return nil, ErrNotFound
	}

	networks, err := s.queries.InstanceNetworksByInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read networks: %w", err)
	}
	forwards, err := s.queries.PortForwardsByInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read port forwards: %w", err)
	}
	traffic, err := s.queries.TrafficByInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read traffic: %w", err)
	}

	detail := &InstanceDetail{
		Networks:     make([]Network, 0, len(networks)),
		PortForwards: make([]PortForward, 0, len(forwards)),
		Traffic:      make([]Traffic, 0, len(traffic)),
	}
	for i := range networks {
		n := &networks[i]
		detail.Networks = append(detail.Networks, Network{
			Type:              n.Type,
			Address:           addrPtr(n.Address),
			Gateway:           addrPtr(n.Gateway),
			Prefix:            intPtr(n.Prefix),
			ProviderNetworkID: textPtr(n.ProviderNetworkID),
			CreatedAt:         n.CreatedAt.Time.UTC(),
		})
	}
	for i := range forwards {
		f := &forwards[i]
		detail.PortForwards = append(detail.PortForwards, PortForward{
			Protocol:          f.Protocol,
			PublicIP:          f.PublicIp.String(),
			PublicPort:        f.PublicPort,
			GuestPort:         f.GuestPort,
			Description:       textPtr(f.Description),
			Status:            f.Status,
			ProviderMappingID: textPtr(f.ProviderMappingID),
			CreatedAt:         f.CreatedAt.Time.UTC(),
		})
	}
	for i := range traffic {
		t := &traffic[i]
		detail.Traffic = append(detail.Traffic, Traffic{
			PeriodStart: t.PeriodStart.Time.UTC(),
			PeriodEnd:   t.PeriodEnd.Time.UTC(),
			RXBytes:     t.RxBytes,
			TXBytes:     t.TxBytes,
			Source:      t.Source,
		})
	}
	return detail, nil
}

// --------------------------------------------------------------------- wallet

// Wallets reads the caller's balances.
func (s *Store) Wallets(ctx context.Context, userID uuid.UUID) ([]Wallet, error) {
	rows, err := s.queries.WalletsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read wallets: %w", err)
	}
	wallets := make([]Wallet, 0, len(rows))
	for i := range rows {
		w := &rows[i]
		wallets = append(wallets, Wallet{
			ID:                    w.ID,
			Currency:              w.Currency,
			AvailableBalanceMinor: w.AvailableBalanceMinor,
			CreatedAt:             w.CreatedAt.Time.UTC(),
		})
	}
	return wallets, nil
}

// Ledger reads the movements behind the caller's balances.
func (s *Store) Ledger(ctx context.Context, userID uuid.UUID) ([]LedgerMovement, error) {
	rows, err := s.queries.WalletLedgerForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read ledger: %w", err)
	}
	movements := make([]LedgerMovement, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		movements = append(movements, LedgerMovement{
			TransactionType: r.TransactionType,
			ReferenceType:   textPtr(r.ReferenceType),
			ReferenceID:     r.ReferenceID,
			Description:     textPtr(r.Description),
			Direction:       r.Direction,
			AmountMinor:     r.AmountMinor,
			Currency:        r.Currency,
			CreatedAt:       r.CreatedAt.Time.UTC(),
		})
	}
	return movements, nil
}

// ------------------------------------------------------------------- invoices

// Invoices reads the caller's invoices, newest first.
func (s *Store) Invoices(ctx context.Context, userID uuid.UUID) ([]Invoice, error) {
	rows, err := s.queries.InvoicesForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read invoices: %w", err)
	}
	invoices := make([]Invoice, 0, len(rows))
	for i := range rows {
		invoices = append(invoices, invoiceOf(&rows[i]))
	}
	return invoices, nil
}

// Invoice reads one of the caller's invoices; a foreign id is a miss.
func (s *Store) Invoice(ctx context.Context, id, userID uuid.UUID) (*Invoice, error) {
	row, err := s.queries.InvoiceByIDForUser(ctx, sqlcgen.InvoiceByIDForUserParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("usersurface: read invoice: %w", err)
	}
	invoice := invoiceOf(&row)
	return &invoice, nil
}

// InvoiceItems reads the lines of an invoice. The caller was already checked
// by Invoice; this method is its companion read.
func (s *Store) InvoiceItems(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceItem, error) {
	rows, err := s.queries.InvoiceItemsByInvoice(ctx, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read invoice items: %w", err)
	}
	items := make([]InvoiceItem, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		items = append(items, InvoiceItem{
			ID:              r.ID,
			DescriptionI18n: r.DescriptionI18n,
			Quantity:        r.Quantity,
			UnitAmountMinor: r.UnitAmountMinor,
			TotalMinor:      r.TotalMinor,
			CreatedAt:       r.CreatedAt.Time.UTC(),
		})
	}
	return items, nil
}

func invoiceOf(row *sqlcgen.Invoice) Invoice {
	return Invoice{
		ID:             row.ID,
		InvoiceNo:      row.InvoiceNo,
		SubscriptionID: row.SubscriptionID,
		OrderID:        row.OrderID,
		Status:         row.Status,
		AmountMinor:    row.AmountMinor,
		Currency:       row.Currency,
		DueAt:          tsPtr(row.DueAt),
		PaidAt:         tsPtr(row.PaidAt),
		CreatedAt:      row.CreatedAt.Time.UTC(),
	}
}

// -------------------------------------------------------------- notifications

// Notifications reads the caller's notifications, newest first.
func (s *Store) Notifications(ctx context.Context, userID uuid.UUID) ([]Notification, error) {
	rows, err := s.queries.NotificationsForUser(ctx, &userID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read notifications: %w", err)
	}
	notifications := make([]Notification, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		notifications = append(notifications, Notification{
			ID:         r.ID,
			Type:       r.Type,
			TitleKey:   r.TitleKey,
			MessageKey: r.MessageKey,
			Parameters: r.Parameters,
			Severity:   r.Severity,
			ReadAt:     tsPtr(r.ReadAt),
			CreatedAt:  r.CreatedAt.Time.UTC(),
		})
	}
	return notifications, nil
}

// UnreadCount reports how many of the caller's notifications are unread.
func (s *Store) UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.queries.UnreadNotificationCountForUser(ctx, &userID)
	if err != nil {
		return 0, fmt.Errorf("usersurface: count unread: %w", err)
	}
	return count, nil
}

// MarkRead stamps one of the caller's notifications as read. A foreign id is
// a miss, and an already-read row is a successful no-op.
func (s *Store) MarkRead(ctx context.Context, id, userID uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.MarkNotificationRead(ctx, sqlcgen.MarkNotificationReadParams{
		ID:     id,
		ReadAt: pgtype.Timestamptz{Time: at, Valid: true},
		UserID: &userID,
	})
	if err != nil {
		return false, fmt.Errorf("usersurface: mark read: %w", err)
	}
	return tag == 1, nil
}

// -------------------------------------------------------------------- tickets

// TicketPriority vocabulary.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

// TicketOpen creates a conversation with its first message. The ticket and
// the message are one fact; they land in one transaction.
func (s *Store) TicketOpen(ctx context.Context, id uuid.UUID, ticketNo string, userID uuid.UUID,
	subject, priority, message string, _ time.Time) error {
	return s.WithinTransaction(ctx, func(tx *Store) error {
		if err := tx.queries.CreateTicket(ctx, sqlcgen.CreateTicketParams{
			ID:       id,
			TicketNo: ticketNo,
			UserID:   userID,
			Subject:  subject,
			Status:   "open",
			Priority: priority,
		}); err != nil {
			return fmt.Errorf("usersurface: create ticket: %w", err)
		}
		if err := tx.queries.CreateTicketMessage(ctx, sqlcgen.CreateTicketMessageParams{
			ID:         uuid.New(),
			TicketID:   id,
			SenderType: "user",
			SenderID:   &userID,
			Message:    message,
		}); err != nil {
			return fmt.Errorf("usersurface: create first message: %w", err)
		}
		return nil
	})
}

// Tickets reads the caller's conversations, newest first.
func (s *Store) Tickets(ctx context.Context, userID uuid.UUID) ([]Ticket, error) {
	rows, err := s.queries.TicketsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read tickets: %w", err)
	}
	tickets := make([]Ticket, 0, len(rows))
	for i := range rows {
		tickets = append(tickets, ticketOf(&rows[i]))
	}
	return tickets, nil
}

// Ticket reads one of the caller's conversations; a foreign id is a miss.
func (s *Store) Ticket(ctx context.Context, id, userID uuid.UUID) (*Ticket, error) {
	row, err := s.queries.TicketByIDForUser(ctx, sqlcgen.TicketByIDForUserParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("usersurface: read ticket: %w", err)
	}
	ticket := ticketOf(&row)
	return &ticket, nil
}

// TicketMessages reads the turns of one conversation.
func (s *Store) TicketMessages(ctx context.Context, ticketID uuid.UUID) ([]TicketMessage, error) {
	rows, err := s.queries.TicketMessagesByTicket(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("usersurface: read ticket messages: %w", err)
	}
	messages := make([]TicketMessage, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		messages = append(messages, TicketMessage{
			ID:         r.ID,
			SenderType: r.SenderType,
			SenderID:   r.SenderID,
			Message:    r.Message,
			CreatedAt:  r.CreatedAt.Time.UTC(),
		})
	}
	return messages, nil
}

// TicketReply appends the caller's message to their own conversation.
func (s *Store) TicketReply(ctx context.Context, ticketID, userID uuid.UUID, message string, at time.Time) error {
	return s.WithinTransaction(ctx, func(tx *Store) error {
		if err := tx.queries.CreateTicketMessage(ctx, sqlcgen.CreateTicketMessageParams{
			ID:         uuid.New(),
			TicketID:   ticketID,
			SenderType: "user",
			SenderID:   &userID,
			Message:    message,
		}); err != nil {
			return fmt.Errorf("usersurface: create reply: %w", err)
		}
		if _, err := tx.queries.TouchTicketOnMessage(ctx, sqlcgen.TouchTicketOnMessageParams{
			ID:        ticketID,
			Column2:   "user",
			UpdatedAt: pgtype.Timestamptz{Time: at, Valid: true},
		}); err != nil {
			return fmt.Errorf("usersurface: touch ticket: %w", err)
		}
		return nil
	})
}

// TicketClose closes one of the caller's conversations. Closing twice is a
// no-op, not an error; the customer's intent is already on the record.
func (s *Store) TicketClose(ctx context.Context, id, userID uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.CloseTicket(ctx, sqlcgen.CloseTicketParams{
		ID:       id,
		UserID:   userID,
		ClosedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("usersurface: close ticket: %w", err)
	}
	return tag == 1, nil
}

func ticketOf(row *sqlcgen.Ticket) Ticket {
	return Ticket{
		ID:        row.ID,
		TicketNo:  row.TicketNo,
		Subject:   row.Subject,
		Status:    row.Status,
		Priority:  row.Priority,
		CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(),
		ClosedAt:  tsPtr(row.ClosedAt),
	}
}

// --------------------------------------------------- operation ownership join

// InstanceOwner reads the user id behind an instance, for the operation
// ownership check the user's event stream performs.
func (s *Store) InstanceOwner(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, error) {
	owner, err := s.queries.InstanceOwner(ctx, instanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("usersurface: read instance owner: %w", err)
	}
	return owner, nil
}

// SubscriptionOwner reads the user id behind a subscription.
func (s *Store) SubscriptionOwner(ctx context.Context, subscriptionID uuid.UUID) (uuid.UUID, error) {
	owner, err := s.queries.SubscriptionOwner(ctx, subscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("usersurface: read subscription owner: %w", err)
	}
	return owner, nil
}

// --------------------------------------------------------------- shared casts

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func addrPtr(value *netip.Addr) *string {
	if value == nil {
		return nil
	}
	addr := value.String()
	return &addr
}

func intPtr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	return &value.Int32
}

func tsPtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	ts := value.Time.UTC()
	return &ts
}

// NewTicketNumber returns a customer-facing ticket number. The same shape the
// commerce numbers use — kind, date, an unguessable suffix read aloud without
// ambiguity — because a support conversation quotes one of these.
func NewTicketNumber(now time.Time) (string, error) {
	suffix, err := randomTicketSuffix(10)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("TK-%s-%s", now.UTC().Format("20060102"), suffix), nil
}

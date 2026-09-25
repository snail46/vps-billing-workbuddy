// The gateway's agent surface (ADR-013 §1): authenticate, heartbeat, claim
// commands, post results. Plain request/response — "online" is last_seen_at
// within a freshness window, and any replica can serve any agent.
package runman

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
)

// Mount installs the agent surface on the router. It is public in chi's sense
// — no session holds it — and authenticated by the node token each handler
// verifies.
func Mount(r chi.Router, store *Store) {
	handler := &gateway{store: store}
	r.Post("/runman/auth", handler.auth)
	r.Post("/runman/heartbeat", handler.authed(handler.heartbeat))
	r.Get("/runman/commands", handler.authed(handler.claim))
	r.Post("/runman/commands/{commandID}/result", handler.authed(handler.result))
}

type gateway struct {
	store *Store
}

// authed verifies the Bearer token and hands the handler the agent row.
func (g *gateway) authed(next func(w http.ResponseWriter, r *http.Request, agent CommandAgent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			httpx.WriteError(w, r, nil, httpx.ErrUnauthorized())
			return
		}
		agent, err := g.store.Authenticate(r.Context(), token)
		if err != nil || agent.Status != "active" {
			httpx.WriteError(w, r, nil, httpx.ErrUnauthorized())
			return
		}
		next(w, r, CommandAgent{ID: agent.ID, NodeID: agent.NodeID})
	}
}

// CommandAgent is the authenticated identity behind one request: the agent
// row's own uuid, and the node it serves as the opaque string the provider
// interface carries.
type CommandAgent struct {
	ID     uuid.UUID
	NodeID string
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

type authRequest struct {
	Token string `json:"token"`
}

type authResponse struct {
	AgentID string `json:"agent_id"`
	NodeID  string `json:"node_id"`
}

func (g *gateway) auth(w http.ResponseWriter, r *http.Request) {
	var body authRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Token == "" {
		httpx.WriteError(w, r, nil, httpx.ErrValidation())
		return
	}
	agent, err := g.store.Authenticate(r.Context(), body.Token)
	if err != nil || agent.Status != "active" {
		httpx.WriteError(w, r, nil, httpx.ErrUnauthorized())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, authResponse{AgentID: agent.ID.String(), NodeID: agent.NodeID})
}

type heartbeatResponse struct {
	Pending int64 `json:"pending"`
}

func (g *gateway) heartbeat(w http.ResponseWriter, r *http.Request, agent CommandAgent) {
	pending, err := g.store.Heartbeat(r.Context(), agent.ID)
	if err != nil {
		httpx.WriteError(w, r, nil, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, heartbeatResponse{Pending: pending})
}

func (g *gateway) claim(w http.ResponseWriter, r *http.Request, agent CommandAgent) {
	commands, err := g.store.Claim(r.Context(), agent.ID)
	if err != nil {
		httpx.WriteError(w, r, nil, httpx.ErrInternal())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"commands": commands})
}

type resultRequest struct {
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"error_code"`
}

func (g *gateway) result(w http.ResponseWriter, r *http.Request, agent CommandAgent) {
	commandID, err := uuid.Parse(chi.URLParam(r, "commandID"))
	if err != nil {
		httpx.WriteError(w, r, nil, httpx.ErrValidation())
		return
	}
	var body resultRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.WriteError(w, r, nil, httpx.ErrValidation())
		return
	}
	if body.Status != StatusSucceeded && body.Status != StatusFailed {
		httpx.WriteError(w, r, nil, httpx.ErrValidation())
		return
	}
	// The result only lands on a command this agent owns; the claim already
	// bound the two, and the ownership check re-asserts it.
	command, lookErr := g.store.Queries().RunmanCommandByID(r.Context(), commandID)
	if errors.Is(lookErr, pgx.ErrNoRows) || (lookErr == nil && command.AgentID != agent.ID) {
		httpx.WriteError(w, r, nil, httpx.ErrNotFound())
		return
	}
	if lookErr != nil {
		httpx.WriteError(w, r, nil, httpx.ErrInternal())
		return
	}
	if err := g.store.Complete(r.Context(), commandID, body.Status, body.Result, body.ErrorCode); err != nil {
		httpx.WriteError(w, r, nil, httpx.ErrConflict())
		return
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"completed": true})
}

// The customer's instance surface: what they bought, as the record holds it.
// The Gate's last step — a user seeing their instance running — reads here.
package httpapi

import (
	"net/http"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
)

// InstanceDeps are the collaborators the customer's instance surface needs.
type InstanceDeps struct {
	Store *instancestore.Store
}

type instancePayload struct {
	ID                 string     `json:"id"`
	SubscriptionID     string     `json:"subscription_id"`
	Name               string     `json:"name"`
	DesiredState       string     `json:"desired_state"`
	ObservedState      string     `json:"observed_state"`
	CPUCores           float64    `json:"cpu_cores"`
	MemoryMB           int64      `json:"memory_mb"`
	DiskGB             int64      `json:"disk_gb"`
	ProviderInstanceID *string    `json:"provider_instance_id"`
	LastSyncedAt       *time.Time `json:"last_synced_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

// listInstances answers with the caller's live instances, newest first.
func (a *api) listMyInstances(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	instances, err := a.instances.Store.ForUser(r.Context(), principal.Session.SubjectID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	payloads := make([]instancePayload, 0, len(instances))
	for i := range instances {
		payloads = append(payloads, newInstancePayload(instances[i]))
	}

	httpx.WriteData(w, r, http.StatusOK, map[string]any{"instances": payloads})
}

func newInstancePayload(inst instancestore.Instance) instancePayload {
	return instancePayload{
		ID:                 inst.ID.String(),
		SubscriptionID:     inst.SubscriptionID.String(),
		Name:               inst.Name,
		DesiredState:       inst.DesiredState,
		ObservedState:      inst.ObservedState,
		CPUCores:           inst.CPUCores,
		MemoryMB:           inst.MemoryMB,
		DiskGB:             inst.DiskGB,
		ProviderInstanceID: inst.ProviderInstanceID,
		LastSyncedAt:       inst.LastSyncedAt,
		CreatedAt:          inst.CreatedAt,
	}
}

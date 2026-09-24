package httpapi

// The infrastructure's administrative surface: read-only, because the domain's
// writers are the scheduler and the workflows that compose it, and provider and
// node creation is an operator act with credentials attached that lands with the
// admin web (ADR-007 §6). Reading is what every other phase needs from this one.

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
)

// InfraDeps are the collaborators the infrastructure surface needs.
type InfraDeps struct {
	Store *infrastore.Store
}

// providerPayload is a provider as the admin API exposes it.
type providerPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// nodeGroupPayload is a group as the admin API exposes it.
type nodeGroupPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
}

// nodePayload is a node with the capacity numbers an operator schedules against.
type nodePayload struct {
	ID          string  `json:"id"`
	ProviderID  string  `json:"provider_id"`
	GroupID     *string `json:"group_id"`
	Name        string  `json:"name"`
	Region      string  `json:"region"`
	Status      string  `json:"status"`
	CPUTotal    float64 `json:"cpu_total"`
	CPUAlloc    float64 `json:"cpu_allocated"`
	CPURsrv     float64 `json:"cpu_reserved"`
	MemTotalMB  int64   `json:"memory_total_mb"`
	MemAllocMB  int64   `json:"memory_allocated_mb"`
	MemRsrvMB   int64   `json:"memory_reserved_mb"`
	DiskTotalGB int64   `json:"disk_total_gb"`
	DiskAllocGB int64   `json:"disk_allocated_gb"`
	DiskRsrvGB  int64   `json:"disk_reserved_gb"`
	Weight      int     `json:"weight"`
	Version     int64   `json:"version"`
}

// listProviders returns the platform's provider records.
func (a *api) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := a.infra.Store.ListProviders(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal().WithCause(err))
		return
	}
	payload := make([]providerPayload, 0, len(providers))
	for i := range providers {
		payload = append(payload, providerPayload{
			ID:     providers[i].ID.String(),
			Name:   providers[i].Name,
			Type:   providers[i].Type,
			Status: providers[i].Status,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"providers": payload})
}

// ListNodeGroups returns the scheduling groups.
func (a *api) listNodeGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.infra.Store.ListNodeGroups(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal().WithCause(err))
		return
	}
	payload := make([]nodeGroupPayload, 0, len(groups))
	for i := range groups {
		payload = append(payload, nodeGroupPayload{
			ID:     groups[i].ID.String(),
			Name:   groups[i].Name,
			Region: groups[i].Region,
			Status: groups[i].Status,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"node_groups": payload})
}

// listNodes returns the nodes with their capacity book.
func (a *api) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.infra.Store.ListNodes(r.Context())
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal().WithCause(err))
		return
	}
	payload := make([]nodePayload, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		var groupID *string
		if node.NodeGroupID != uuid.Nil {
			value := node.NodeGroupID.String()
			groupID = &value
		}
		payload = append(payload, nodePayload{
			ID:          node.ID.String(),
			ProviderID:  node.ProviderID.String(),
			GroupID:     groupID,
			Name:        node.Name,
			Region:      node.Region,
			Status:      node.Status,
			CPUTotal:    node.CPUTotal,
			CPUAlloc:    node.CPUAllocated,
			CPURsrv:     node.CPURserved,
			MemTotalMB:  node.MemoryTotalMB,
			MemAllocMB:  node.MemoryAllocMB,
			MemRsrvMB:   node.MemoryRsrvMB,
			DiskTotalGB: node.DiskTotalGB,
			DiskAllocGB: node.DiskAllocGB,
			DiskRsrvGB:  node.DiskRsrvGB,
			Weight:      node.Weight,
			Version:     node.Version,
		})
	}
	httpx.WriteData(w, r, http.StatusOK, map[string]any{"nodes": payload})
}

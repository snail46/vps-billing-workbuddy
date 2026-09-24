package infra

// The infrastructure domain: providers, node groups, nodes, and the two
// primitives the provisioning workflow builds on — a deterministic scheduler
// and a capacity reservation (ADR-007).

import (
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// Node statuses — docs/05's node machine.
const (
	NodeOnline      = "online"
	NodeDegraded    = "degraded"
	NodeDraining    = "draining"
	NodeMaintenance = "maintenance"
	NodeOffline     = "offline"
)

// Group and provider statuses — on/off switches (ADR-007 §1).
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// NodeGroup is a scheduling domain: the plans that name a group land on its nodes.
type NodeGroup struct {
	ID     uuid.UUID
	Name   string
	Region string
	Status string
}

// Node is one provider's one machine, with the capacity the platform books
// against it. The three paired counters are denormalisation whose audit is the
// reconciler: allocated is rebuildable from instances, reserved from in-flight
// operations (ADR-007 §2).
type Node struct {
	ID             uuid.UUID
	ProviderID     uuid.UUID
	NodeGroupID    uuid.UUID
	ProviderNodeID string
	Name           string
	Region         string
	Status         string
	CPUTotal       float64
	MemoryTotalMB  int64
	DiskTotalGB    int64
	CPUAllocated   float64
	MemoryAllocMB  int64
	DiskAllocGB    int64
	CPURserved     float64
	MemoryRsrvMB   int64
	DiskRsrvGB     int64
	Weight         int
	Capabilities   provider.Capabilities
	Version        int64
}

// Spec is what one instance needs, in the plan's own units.
type Spec struct {
	CPUCores       float64
	MemoryMB       int64
	DiskGB         int64
	Virtualization string
}

// FreeCapacity reports what the node can still promise.
func (n Node) FreeCapacity() (cpu float64, memoryMB, diskGB int64) {
	return n.CPUTotal - n.CPUAllocated - n.CPURserved,
		n.MemoryTotalMB - n.MemoryAllocMB - n.MemoryRsrvMB,
		n.DiskTotalGB - n.DiskAllocGB - n.DiskRsrvGB
}

// SupportsSpec reports whether the node can run the spec.
//
// An empty SupportedRuntimes list is no constraint — the field is advisory, and
// a node whose capabilities predate the field should not strand every plan.
// A non-empty list is a whitelist.
func (n Node) SupportsSpec(spec Spec) bool {
	runtimes := n.Capabilities.SupportedRuntimes
	if len(runtimes) == 0 {
		return true
	}
	for _, runtime := range runtimes {
		if runtime == spec.Virtualization {
			return true
		}
	}
	return false
}

// Errors reported by the domain.
var (
	// ErrNoNodeAvailable reports that no node in the group can take the spec.
	// It says why in the message, because "no space" and "the group is empty"
	// have different remedies.
	ErrNoNodeAvailable = errors.New("infra: no node in the group can take this spec")
	// ErrGroupNotActive reports scheduling into a disabled group.
	ErrGroupNotActive = errors.New("infra: the node group is not active")
	// ErrInsufficientCapacity reports a reservation whose node cannot cover it.
	ErrInsufficientCapacity = errors.New("infra: the node's free capacity does not cover the spec")
	// ErrNodeNotOnline reports a reservation against a node that cannot take one.
	ErrNodeNotOnline = errors.New("infra: the node is not online")
	// ErrReservationLost reports a commit or release whose reservation is not on
	// the row — already committed, already released, or never made.
	ErrReservationLost = errors.New("infra: no such reservation on the node")
)

// SelectNode picks the node a spec lands on (ADR-007 §3).
//
// Deterministic on purpose: the candidates are ordered by load ratio (lowest
// first — the platform spreads load rather than filling racks), then weight
// (highest first — the operator's hand), then id, and the first one that fits
// wins. The same database state always yields the same node, which is what
// makes a reconcile's re-selection a no-op and the unit tests table-driven.
func SelectNode(nodes []Node, spec Spec) (Node, error) {
	candidates := make([]Node, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		if node.Status != NodeOnline {
			continue
		}
		if !node.SupportsSpec(spec) {
			continue
		}
		cpu, mem, disk := node.FreeCapacity()
		if cpu >= spec.CPUCores && mem >= spec.MemoryMB && disk >= spec.DiskGB {
			candidates = append(candidates, *node)
		}
	}
	if len(candidates) == 0 {
		return Node{}, ErrNoNodeAvailable
	}

	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if ra, rb := a.loadRatio(), b.loadRatio(); ra != rb {
			return ra < rb
		}
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return a.ID.String() < b.ID.String()
	})
	return candidates[0], nil
}

// loadRatio is the scheduler's first key: how committed the node already is.
// A node with no capacity recorded has nothing to load, and it never wins —
// but it also never divides by zero.
func (n Node) loadRatio() float64 {
	if n.CPUTotal <= 0 {
		return 2 // beyond any real ratio, so a real node always sorts first
	}
	committed := n.CPUAllocated + n.CPURserved
	if committed < 0 {
		committed = 0
	}
	return committed / n.CPUTotal
}

// DescribeFree renders a node's free capacity for the error message an
// operator reads when nothing fits: the amounts are what they have to work from.
func DescribeFree(n Node) string {
	cpu, mem, disk := n.FreeCapacity()
	return fmt.Sprintf("%s: %g vCPU / %d MB / %d GB free", n.Name, cpu, mem, disk)
}

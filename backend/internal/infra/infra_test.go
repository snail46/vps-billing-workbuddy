package infra_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
)

// The scheduler's tests are table-driven because the scheduler is deterministic
// (ADR-007 §3): the same nodes and the same spec always pick the same node, and
// docs/18 asks for scheduler unit tests precisely so the choice never has to be
// discovered by running the platform.

func node(name string, cpu float64, weight int, status string) infra.Node {
	return infra.Node{
		ID:            uuid.MustParse(name),
		Name:          name,
		Status:        status,
		CPUTotal:      cpu,
		MemoryTotalMB: 4096,
		DiskTotalGB:   80,
		Weight:        weight,
	}
}

var spec = infra.Spec{CPUCores: 2, MemoryMB: 1024, DiskGB: 20, Virtualization: "kvm"}

func TestSelectNodePicksTheLeastLoadedNode(t *testing.T) {
	empty := node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOnline)
	busy := node("00000000-0000-7000-8000-000000000002", 8, 100, infra.NodeOnline)
	busy.CPUAllocated = 6 // 75% committed against the empty node's 0%

	chosen, err := infra.SelectNode([]infra.Node{busy, empty}, spec)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if chosen.ID != empty.ID {
		t.Errorf("the scheduler chose %q; the empty node spreads the load", chosen.Name)
	}
}

func TestSelectNodeBreaksTiesByWeightThenID(t *testing.T) {
	// Two equally loaded nodes: the operator's weight decides.
	heavy := node("00000000-0000-7000-8000-000000000001", 8, 200, infra.NodeOnline)
	light := node("00000000-0000-7000-8000-000000000002", 8, 50, infra.NodeOnline)
	chosen, err := infra.SelectNode([]infra.Node{light, heavy}, spec)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if chosen.ID != heavy.ID {
		t.Errorf("the scheduler chose %q; the heavier weight should win the tie", chosen.Name)
	}

	// Two identical nodes in every measure but id: the lower id wins, and the
	// order the caller presented them in does not matter.
	first := node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOnline)
	second := node("00000000-0000-7000-8000-000000000002", 8, 100, infra.NodeOnline)
	for _, order := range [][]infra.Node{{first, second}, {second, first}} {
		chosen, err := infra.SelectNode(order, spec)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if chosen.ID != first.ID {
			t.Errorf("the scheduler chose %q; the lower id breaks a full tie", chosen.Name)
		}
	}
}

func TestSelectNodeSkipsNodesThatCannotTakeTheSpec(t *testing.T) {
	offline := node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOffline)
	draining := node("00000000-0000-7000-8000-000000000002", 8, 100, infra.NodeDraining)
	full := node("00000000-0000-7000-8000-000000000003", 8, 100, infra.NodeOnline)
	full.CPUAllocated = 8 // nothing free, however empty it looks
	reserved := node("00000000-0000-7000-8000-000000000004", 8, 100, infra.NodeOnline)
	reserved.CPURserved = 8 // promised to in-flight work, which is the same as spent
	unsupported := node("00000000-0000-7000-8000-000000000005", 8, 100, infra.NodeOnline)
	unsupported.Capabilities.SupportedRuntimes = []string{"lxc"} // the spec asks for kvm

	fits := node("00000000-0000-7000-8000-000000000006", 8, 10, infra.NodeOnline)
	chosen, err := infra.SelectNode([]infra.Node{
		offline, draining, full, reserved, unsupported, fits,
	}, spec)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if chosen.ID != fits.ID {
		t.Errorf("the scheduler chose %q; only %q can take the spec", chosen.Name, fits.Name)
	}
}

func TestSelectNodeReportsWhenNothingFits(t *testing.T) {
	full := node("00000000-0000-7000-8000-000000000001", 4, 100, infra.NodeOnline)
	full.CPUAllocated = 4

	if _, err := infra.SelectNode([]infra.Node{full}, spec); !errors.Is(err, infra.ErrNoNodeAvailable) {
		t.Fatalf("a group with nothing free produced %v", err)
	}
	// An empty group is the same answer, not a different error to handle.
	if _, err := infra.SelectNode(nil, spec); !errors.Is(err, infra.ErrNoNodeAvailable) {
		t.Fatalf("an empty group produced %v", err)
	}
}

func TestSelectNodeIsDeterministicAcrossManyRuns(t *testing.T) {
	nodes := []infra.Node{
		node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOnline),
		node("00000000-0000-7000-8000-000000000002", 8, 100, infra.NodeOnline),
		node("00000000-0000-7000-8000-000000000003", 8, 100, infra.NodeOnline),
	}
	first, err := infra.SelectNode(nodes, spec)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	// A reconcile re-running selection on unchanged state must choose the same
	// node; fifty passes make an ordering accident visible.
	for i := 0; i < 50; i++ {
		chosen, err := infra.SelectNode(nodes, spec)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if chosen.ID != first.ID {
			t.Fatalf("run %d chose %q after the first chose %q; the choice is not deterministic",
				i, chosen.Name, first.Name)
		}
	}
}

func TestFreeCapacitySubtractsAllocatedAndReserved(t *testing.T) {
	n := node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOnline)
	n.CPUAllocated = 3
	n.CPURserved = 2
	n.MemoryAllocMB = 1024
	n.MemoryRsrvMB = 512
	n.DiskAllocGB = 40

	cpu, mem, disk := n.FreeCapacity()
	if cpu != 3 || mem != 4096-1024-512 || disk != 40 {
		t.Errorf("free capacity = (%g, %d, %d), expected (3, 2560, 40)", cpu, mem, disk)
	}
}

func TestSupportsSpecTreatsAnEmptyRuntimeListAsUnconstrained(t *testing.T) {
	n := node("00000000-0000-7000-8000-000000000001", 8, 100, infra.NodeOnline)
	if !n.SupportsSpec(spec) {
		t.Error("a node with no recorded runtimes supports everything; a field the row predates must not strand plans")
	}
	n.Capabilities.SupportedRuntimes = []string{"lxc"}
	if n.SupportsSpec(spec) {
		t.Error("a node that whitelists lxc cannot run a kvm spec")
	}
}

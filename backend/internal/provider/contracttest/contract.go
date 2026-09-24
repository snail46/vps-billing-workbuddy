package contracttest

// The provider contract's proof.
//
// `docs/06` fixes the interface, the error codes and the idempotency rule; this
// suite turns them into tests that any implementation must pass — the mock here,
// Phase 7's direct provider against the real thing later. A provider that skips
// the suite has not proven anything about the contract, and a suite that lived
// inside the mock's own test file could not be handed to the next provider.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// Factory builds a fresh provider for one suite run.
type Factory func(t *testing.T) provider.Provider

// RunContractTests executes the whole suite against one provider.
func RunContractTests(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("health and capabilities", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		health, err := p.Health(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if health.Status == "" {
			t.Error("health carries no status")
		}

		capabilities, err := p.Capabilities(ctx)
		if err != nil {
			t.Fatalf("capabilities: %v", err)
		}
		if !capabilities.CreateInstance || !capabilities.DeleteInstance {
			t.Error("a provider that cannot create or delete instances cannot serve the platform")
		}
	})

	t.Run("images are listed", func(t *testing.T) {
		p := factory(t)
		images, err := p.ListImages(context.Background(), "node-1")
		if err != nil {
			t.Fatalf("list images: %v", err)
		}
		if len(images) == 0 {
			t.Fatal("a provider with no images cannot provision anything")
		}
		for _, image := range images {
			if image.ID == "" || image.OS == "" {
				t.Errorf("image %+v carries no identifier or OS", image)
			}
		}
	})

	t.Run("create is idempotent by key", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		req := provider.CreateInstanceRequest{
			OperationID:    "op-1",
			IdempotencyKey: "create-contract-1",
			NodeID:         "node-1",
			Name:           "contract-1",
			CPUCores:       1,
			MemoryMB:       1024,
			DiskGB:         20,
			Image:          "debian-12",
			Virtualization: "kvm",
		}

		first, err := p.CreateInstance(ctx, req)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if first == nil || !first.Accepted {
			t.Fatalf("the first create was not accepted: %+v", first)
		}

		// The same key, sent again — a retry, a timeout re-run, a worker restart —
		// must land on the instance the first create made, never a second one.
		second, err := p.CreateInstance(ctx, req)
		if err != nil {
			t.Fatalf("retried create: %v", err)
		}
		if second.ProviderOperationID != first.ProviderOperationID {
			t.Errorf("a retried create produced operation %q, expected %q",
				second.ProviderOperationID, first.ProviderOperationID)
		}

		instanceID, _ := first.Metadata["instance_id"].(string)
		if instanceID == "" {
			t.Fatal("the accepted create carries no instance identifier")
		}

		instance, err := p.GetInstance(ctx, provider.GetInstanceRequest{
			NodeID:             "node-1",
			ProviderInstanceID: instanceID,
		})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if instance.State == "" {
			t.Error("the instance carries no state")
		}
	})

	t.Run("create without a key is refused", func(t *testing.T) {
		p := factory(t)
		_, err := p.CreateInstance(context.Background(), provider.CreateInstanceRequest{
			OperationID: "op-2",
			NodeID:      "node-1",
			Name:        "contract-2",
			CPUCores:    1,
			MemoryMB:    1024,
			DiskGB:      20,
			Image:       "debian-12",
		})
		if err == nil {
			t.Fatal("a create without an idempotency key was accepted; the retry rule needs the key")
		}
		assertProviderError(t, err, "UNKNOWN_PROVIDER_ERROR")
	})

	t.Run("lifecycle actions move the state", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		instanceID := createdInstance(t, p, "lifecycle-1")

		// Stop, then start: the actions whose from-state the contract fixes.
		if _, err := p.StopInstance(ctx, provider.InstanceActionRequest{
			IdempotencyKey: "lifecycle-1-stop", ProviderInstanceID: instanceID,
		}); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if _, err := p.StartInstance(ctx, provider.InstanceActionRequest{
			IdempotencyKey: "lifecycle-1-start", ProviderInstanceID: instanceID,
		}); err != nil {
			t.Fatalf("start: %v", err)
		}

		// An action against an instance that is not there is the contract's own
		// error code, not a generic failure.
		_, err := p.StopInstance(ctx, provider.InstanceActionRequest{
			IdempotencyKey: "lifecycle-1-ghost", ProviderInstanceID: "no-such-instance",
		})
		assertProviderError(t, err, "INSTANCE_NOT_FOUND")
	})

	t.Run("delete is idempotent and removes the instance", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		instanceID := createdInstance(t, p, "delete-1")
		req := provider.InstanceActionRequest{
			IdempotencyKey: "delete-1", ProviderInstanceID: instanceID,
		}
		if _, err := p.DeleteInstance(ctx, req); err != nil {
			t.Fatalf("delete: %v", err)
		}
		// The second delete meets an instance that is already gone. Whether the
		// provider answers success or INSTANCE_NOT_FOUND, it must not invent a
		// new instance; the platform's own delete flow is idempotent over both.
		if _, err := p.DeleteInstance(ctx, req); err != nil {
			assertProviderError(t, err, "INSTANCE_NOT_FOUND")
		}
		if _, err := p.GetInstance(ctx, provider.GetInstanceRequest{
			ProviderInstanceID: instanceID,
		}); !isNotFound(err) {
			t.Errorf("a deleted instance is still readable: %v", err)
		}
	})

	t.Run("usage and traffic answer for a live instance", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		instanceID := createdInstance(t, p, "usage-1")
		usage, err := p.GetUsage(ctx, provider.GetInstanceRequest{ProviderInstanceID: instanceID})
		if err != nil {
			t.Fatalf("usage: %v", err)
		}
		if usage.MemoryTotalMB <= 0 {
			t.Errorf("usage reports %d MB total memory", usage.MemoryTotalMB)
		}
		now := time.Now()
		traffic, err := p.GetTraffic(ctx, provider.GetTrafficRequest{
			GetInstanceRequest: provider.GetInstanceRequest{ProviderInstanceID: instanceID},
			From:               now.Add(-time.Hour),
			To:                 now,
		})
		if err != nil {
			t.Fatalf("traffic: %v", err)
		}
		if traffic.From.IsZero() {
			t.Error("traffic carries no window")
		}
	})

	t.Run("port forwards round-trip", func(t *testing.T) {
		p := factory(t)
		ctx := context.Background()

		instanceID := createdInstance(t, p, "fwd-1")
		operation, err := p.AddPortForward(ctx, provider.AddPortForwardRequest{
			InstanceActionRequest: provider.InstanceActionRequest{
				IdempotencyKey: "fwd-1", ProviderInstanceID: instanceID,
			},
			Protocol:   "tcp",
			PublicPort: 22022,
			GuestPort:  22,
		})
		if err != nil {
			t.Fatalf("add port forward: %v", err)
		}
		if mappingID, _ := operation.Metadata["mapping_id"].(string); mappingID == "" {
			t.Fatal("the accepted forward carries no mapping identifier")
		}
	})
}

// createdInstance walks a suite test to a live instance and returns its
// provider-side identifier.
func createdInstance(t *testing.T, p provider.Provider, key string) string {
	t.Helper()
	operation, err := p.CreateInstance(context.Background(), provider.CreateInstanceRequest{
		OperationID:    "op-" + key,
		IdempotencyKey: "contract-" + key,
		NodeID:         "node-1",
		Name:           key,
		CPUCores:       1,
		MemoryMB:       1024,
		DiskGB:         20,
		Image:          "debian-12",
		Virtualization: "kvm",
	})
	if err != nil {
		t.Fatalf("create %s: %v", key, err)
	}
	instanceID, _ := operation.Metadata["instance_id"].(string)
	if instanceID == "" {
		t.Fatalf("the create for %s carries no instance identifier", key)
	}
	return instanceID
}

// assertProviderError asserts the error is the contract's own type carrying the
// expected code — a plain error cannot name PROVIDER_TIMEOUT, and a caller that
// cannot distinguish codes cannot retry safely.
func assertProviderError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a provider error with code %s, got none", code)
	}
	var perr *provider.Error
	if !errors.As(err, &perr) {
		t.Fatalf("expected a *provider.Error for code %s, got %T: %v", code, err, err)
	}
	if perr.Code != code {
		t.Fatalf("expected code %s, got %s (%s)", code, perr.Code, perr.RawCode)
	}
}

func isNotFound(err error) bool {
	var perr *provider.Error
	return errors.As(err, &perr) && perr.Code == "INSTANCE_NOT_FOUND"
}

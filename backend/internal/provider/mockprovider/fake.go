package mockprovider

// A provider that exists to be tested against.
//
// It implements the whole contract in memory: instances live in a map, actions
// flip their states, and every call is recorded so a workflow test can assert
// what was asked of the provider rather than what it claims happened. It is
// configurable to fail — later resilience tests need a provider that times out
// on demand — and its CreateInstance is idempotent by key, which is the part of
// the contract the contract test holds hardest (docs/06).

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// instance is the mock's own record of one customer machine.
type instance struct {
	spec      provider.CreateInstanceRequest
	state     string
	ipv4      []string
	createdAt time.Time
}

// Fake is an in-memory provider.
//
// It is safe for concurrent use: workflow tests run steps in parallel, and a
// provider that needs external locking to survive that would be measuring the
// test, not the contract.
type Fake struct {
	name string
	mu   sync.Mutex

	instances map[string]*instance // by idempotency key
	byID      map[string]*instance // by provider instance id
	calls     []string
	failures  map[string]error // method name -> the error to return

	now func() time.Time
}

// New returns a healthy fake with no instances.
func New(name string) *Fake {
	return &Fake{
		name:      name,
		instances: map[string]*instance{},
		byID:      map[string]*instance{},
		failures:  map[string]error{},
		now:       time.Now,
	}
}

// Name implements provider.Provider.
func (f *Fake) Name() string { return f.name }

// FailWhen makes the named method return the error until cleared.
func (f *Fake) FailWhen(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failures, method)
		return
	}
	f.failures[method] = err
}

// Calls returns the recorded method names, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]string, len(f.calls))
	copy(calls, f.calls)
	return calls
}

// InstanceCount reports how many instances the mock holds.
func (f *Fake) InstanceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID)
}

func (f *Fake) record(method string) error {
	f.calls = append(f.calls, method)
	return f.failures[method]
}

// Health implements provider.Provider.
func (f *Fake) Health(context.Context) (*provider.Health, error) {
	if err := f.record("Health"); err != nil {
		return nil, err
	}
	return &provider.Health{
		Status:    "healthy",
		Version:   "mock/1.0",
		CheckedAt: f.now(),
		Details:   map[string]any{"mock": true},
	}, nil
}

// Capabilities implements provider.Provider.
func (f *Fake) Capabilities(context.Context) (*provider.Capabilities, error) {
	if err := f.record("Capabilities"); err != nil {
		return nil, err
	}
	return &provider.Capabilities{
		CreateInstance:    true,
		DeleteInstance:    true,
		Start:             true,
		Stop:              true,
		Restart:           true,
		Reinstall:         true,
		ResetPassword:     true,
		Traffic:           true,
		SupportedRuntimes: []string{"kvm"},
	}, nil
}

// ListImages implements provider.Provider.
func (f *Fake) ListImages(context.Context, string) ([]provider.Image, error) {
	if err := f.record("ListImages"); err != nil {
		return nil, err
	}
	return []provider.Image{
		{ID: "debian-12", Name: "Debian 12", OS: "debian", Version: "12", Arch: "amd64"},
		{ID: "ubuntu-24.04", Name: "Ubuntu 24.04", OS: "ubuntu", Version: "24.04", Arch: "amd64"},
	}, nil
}

// CreateInstance implements provider.Provider.
//
// Idempotent by key: the same request key returns the same accepted operation
// and never a second instance, because a retried creation is the workflow's
// normal path, not an accident (docs/06).
func (f *Fake) CreateInstance(_ context.Context, req provider.CreateInstanceRequest) (*provider.Operation, error) {
	if err := f.record("CreateInstance"); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, &provider.Error{
			Code:       "UNKNOWN_PROVIDER_ERROR",
			Provider:   f.name,
			RawCode:    "missing_key",
			RawMessage: "CreateInstance requires an idempotency key",
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.instances[req.IdempotencyKey]; ok {
		return &provider.Operation{
			ProviderOperationID: "mock-op-" + req.IdempotencyKey,
			Status:              "succeeded",
			Accepted:            true,
			Metadata:            map[string]any{"instance_id": f.providerIDOf(existing)},
		}, nil
	}

	state := "running" // the mock provisions instantly; waiting is the workflow's business
	inst := &instance{
		spec:      req,
		state:     state,
		createdAt: f.now(),
	}
	providerID := "mock-" + req.IdempotencyKey
	if req.IPv4Count > 0 {
		inst.ipv4 = []string{fmt.Sprintf("203.0.113.%d", len(f.byID)%254+1)}
	}
	f.instances[req.IdempotencyKey] = inst
	f.byID[providerID] = inst

	return &provider.Operation{
		ProviderOperationID: "mock-op-" + req.IdempotencyKey,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"instance_id": providerID},
	}, nil
}

func (f *Fake) providerIDOf(inst *instance) string {
	for id, candidate := range f.byID {
		if candidate == inst {
			return id
		}
	}
	return ""
}

// lookup resolves an action's target and records the call.
func (f *Fake) lookup(method string, req provider.InstanceActionRequest) (*instance, error) {
	if err := f.record(method); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[req.ProviderInstanceID]
	if !ok {
		return nil, &provider.Error{
			Code:      "INSTANCE_NOT_FOUND",
			Retryable: false,
			Provider:  f.name,
			RawCode:   "no_such_instance",
		}
	}
	return inst, nil
}

// GetInstance implements provider.Provider.
func (f *Fake) GetInstance(_ context.Context, req provider.GetInstanceRequest) (*provider.Instance, error) {
	if err := f.record("GetInstance"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[req.ProviderInstanceID]
	if !ok {
		return nil, &provider.Error{
			Code:     "INSTANCE_NOT_FOUND",
			Provider: f.name,
			RawCode:  "no_such_instance",
		}
	}
	return f.snapshot(inst), nil
}

func (f *Fake) snapshot(inst *instance) *provider.Instance {
	return &provider.Instance{
		ProviderInstanceID: f.providerIDOf(inst),
		State:              inst.state,
		CPUCores:           inst.spec.CPUCores,
		MemoryMB:           inst.spec.MemoryMB,
		DiskGB:             inst.spec.DiskGB,
		IPv4:               inst.ipv4,
		CreatedAt:          inst.createdAt,
	}
}

// act runs a state-changing action through the shared transition rules.
func (f *Fake) act(method string, req provider.InstanceActionRequest, from, to string) (*provider.Operation, error) {
	inst, err := f.lookup(method, req)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst.state != from {
		return nil, &provider.Error{
			Code:       "UNKNOWN_PROVIDER_ERROR",
			Provider:   f.name,
			RawCode:    fmt.Sprintf("state_is_%s", inst.state),
			RawMessage: fmt.Sprintf("%s requires state %s, instance is %s", method, from, inst.state),
		}
	}
	inst.state = to
	return &provider.Operation{
		ProviderOperationID: "mock-op-" + req.IdempotencyKey,
		Status:              "succeeded",
		Accepted:            true,
	}, nil
}

// StartInstance implements provider.Provider.
func (f *Fake) StartInstance(_ context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return f.act("StartInstance", req, "stopped", "running")
}

// StopInstance implements provider.Provider.
func (f *Fake) StopInstance(_ context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return f.act("StopInstance", req, "running", "stopped")
}

// RestartInstance implements provider.Provider.
func (f *Fake) RestartInstance(_ context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return f.act("RestartInstance", req, "running", "running")
}

// ReinstallInstance implements provider.Provider.
func (f *Fake) ReinstallInstance(_ context.Context, req provider.ReinstallInstanceRequest) (*provider.Operation, error) {
	return f.act("ReinstallInstance", req.InstanceActionRequest, "stopped", "running")
}

// ResetPassword implements provider.Provider.
func (f *Fake) ResetPassword(_ context.Context, req provider.ResetPasswordRequest) (*provider.Operation, error) {
	if _, err := f.lookup("ResetPassword", req.InstanceActionRequest); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &provider.Operation{
		ProviderOperationID: "mock-op-password-" + req.IdempotencyKey,
		Status:              "succeeded",
		Accepted:            true,
	}, nil
}

// DeleteInstance implements provider.Provider.
func (f *Fake) DeleteInstance(_ context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	if err := f.record("DeleteInstance"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[req.ProviderInstanceID]
	if !ok {
		return nil, &provider.Error{
			Code:     "INSTANCE_NOT_FOUND",
			Provider: f.name,
			RawCode:  "no_such_instance",
		}
	}
	// Deleting twice is the same outcome as deleting once — idempotent, like
	// CreateInstance, because the workflow retries.
	delete(f.byID, req.ProviderInstanceID)
	for key, candidate := range f.instances {
		if candidate == inst {
			delete(f.instances, key)
		}
	}
	return &provider.Operation{
		ProviderOperationID: "mock-op-" + req.IdempotencyKey,
		Status:              "succeeded",
		Accepted:            true,
	}, nil
}

// GetUsage implements provider.Provider.
func (f *Fake) GetUsage(_ context.Context, req provider.GetInstanceRequest) (*provider.Usage, error) {
	if err := f.record("GetUsage"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[req.ProviderInstanceID]
	if !ok {
		return nil, &provider.Error{Code: "INSTANCE_NOT_FOUND", Provider: f.name}
	}
	return &provider.Usage{
		CPUPercent:    12.5,
		MemoryUsedMB:  inst.spec.MemoryMB / 4,
		MemoryTotalMB: inst.spec.MemoryMB,
		DiskUsedGB:    float64(inst.spec.DiskGB) / 10,
		DiskTotalGB:   float64(inst.spec.DiskGB),
		ObservedAt:    f.now(),
	}, nil
}

// GetTraffic implements provider.Provider.
func (f *Fake) GetTraffic(_ context.Context, req provider.GetTrafficRequest) (*provider.Traffic, error) {
	if err := f.record("GetTraffic"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[req.ProviderInstanceID]; !ok {
		return nil, &provider.Error{Code: "INSTANCE_NOT_FOUND", Provider: f.name}
	}
	return &provider.Traffic{From: req.From, To: req.To}, nil
}

// ListPortForwards implements provider.Provider.
func (f *Fake) ListPortForwards(_ context.Context, _ provider.GetInstanceRequest) ([]provider.PortForward, error) {
	if err := f.record("ListPortForwards"); err != nil {
		return nil, err
	}
	return nil, nil // the mock forwards nothing until asked
}

// AddPortForward implements provider.Provider.
func (f *Fake) AddPortForward(_ context.Context, req provider.AddPortForwardRequest) (*provider.Operation, error) {
	if err := f.record("AddPortForward"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[req.ProviderInstanceID]; !ok {
		return nil, &provider.Error{Code: "INSTANCE_NOT_FOUND", Provider: f.name}
	}
	return &provider.Operation{
		ProviderOperationID: fmt.Sprintf("mock-fwd-%s-%d", req.ProviderInstanceID, req.PublicPort),
		Status:              "succeeded",
		Accepted:            true,
		Metadata: map[string]any{
			"mapping_id": fmt.Sprintf("mock-mapping-%s-%d", req.ProviderInstanceID, req.PublicPort),
		},
	}, nil
}

// DeletePortForward implements provider.Provider.
func (f *Fake) DeletePortForward(_ context.Context, req provider.DeletePortForwardRequest) (*provider.Operation, error) {
	if err := f.record("DeletePortForward"); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "mock-fwd-deleted-" + req.ProviderMappingID,
		Status:              "succeeded",
		Accepted:            true,
	}, nil
}

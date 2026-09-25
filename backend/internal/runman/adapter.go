// The runman adapter (ADR-013 §3): docs/06's provider interface translated
// into commands the node's agent claims and answers. The adapter depends on
// the provider package and its own store — never on the business core.
package runman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// Defaults a wait rides: the agent is one long-poll away, and a wait that
// outlives its budget is the operation engine's backoff problem, not a
// worker's frozen loop.
const (
	DefaultWaitTimeout = 10 * time.Second
	waitPollInterval   = 100 * time.Millisecond
)

// Error vocabulary, mapped once at this boundary (docs/06).
const (
	errTimeout     = "PROVIDER_TIMEOUT"
	errUnreachable = "PROVIDER_UNAVAILABLE"
	errRejected    = "PROVIDER_COMMAND_REJECTED"
	errNoAgent     = "PROVIDER_UNAVAILABLE"
)

// Adapter is the agent provider.
type Adapter struct {
	store      *Store
	name       string
	waitBudget time.Duration

	// placement remembers which node each created instance lives on. The
	// provider interface's read calls may arrive without a node id (the
	// platform's own record names the node once, not per read), and the
	// adapter is the one place that still knows the route.
	placementMu sync.Mutex
	placement   map[string]string
}

// Build builds the adapter over the gateway's store. (The store's own
// constructor owns the name New in this package.)
func Build(store *Store, name string) *Adapter {
	return &Adapter{store: store, name: name, waitBudget: DefaultWaitTimeout, placement: map[string]string{}}
}

// rememberNode records where a created instance was placed.
func (a *Adapter) rememberNode(providerInstanceID, nodeID string) {
	a.placementMu.Lock()
	defer a.placementMu.Unlock()
	a.placement[providerInstanceID] = nodeID
}

// nodeFor resolves the request's node, falling back to the placement the
// adapter recorded at create time.
func (a *Adapter) nodeFor(req provider.GetInstanceRequest) string {
	if req.NodeID != "" {
		return req.NodeID
	}
	a.placementMu.Lock()
	defer a.placementMu.Unlock()
	return a.placement[req.ProviderInstanceID]
}

// agentForInstance resolves the agent for one instance read or action. When
// neither the request nor the placement names a node, this provider has
// never heard of the instance — the contract's own not-found, not a
// transport failure (docs/06: an unknown instance is not an outage).
func (a *Adapter) agentForInstance(ctx context.Context, req provider.GetInstanceRequest) (uuid.UUID, error) {
	nodeID := a.nodeFor(req)
	if nodeID == "" {
		return uuid.Nil, &provider.Error{Code: "INSTANCE_NOT_FOUND", Provider: a.name, RawMessage: "no placement recorded for instance"}
	}
	return a.agentFor(ctx, nodeID)
}

// Name implements provider.Provider.
func (a *Adapter) Name() string { return a.name }

// agentFor resolves the node's active agent; a node without one is a provider
// that cannot be reached, which is what the error says. The node id is the
// opaque string the interface carries — the registry keys on it as-is.
func (a *Adapter) agentFor(ctx context.Context, nodeID string) (uuid.UUID, error) {
	agent, err := a.store.Queries().ActiveRunmanAgentByNode(ctx, nodeID)
	if err != nil {
		return uuid.Nil, &provider.Error{Code: errNoAgent, Provider: a.name, RawMessage: "no active agent for node"}
	}
	return agent.ID, nil
}

// dispatch enqueues one command and waits for its terminal state.
func (a *Adapter) dispatch(ctx context.Context, agentID uuid.UUID, cmdType, idempotencyKey string, payload any) (json.RawMessage, error) {
	if idempotencyKey == "" {
		// The contract's own refusal: a command that cannot be redelivered
		// safely is not a command this provider accepts (docs/06).
		return nil, &provider.Error{Code: "UNKNOWN_PROVIDER_ERROR", Provider: a.name, RawMessage: "missing idempotency key"}
	}
	command, err := a.store.Enqueue(ctx, agentID, cmdType, idempotencyKey, payload)
	if err != nil {
		return nil, &provider.Error{Code: errUnreachable, Provider: a.name, RawMessage: err.Error()}
	}
	final, err := a.store.Wait(ctx, command.IdempotencyKey, a.waitBudget)
	if err != nil {
		return nil, &provider.Error{Code: errTimeout, Provider: a.name, RawMessage: err.Error(), Retryable: true}
	}
	switch final.Status {
	case StatusSucceeded:
		return final.Result, nil
	case StatusFailed:
		code := errRejected
		if final.ErrorCode.Valid {
			code = final.ErrorCode.String
		}
		return nil, &provider.Error{Code: code, Provider: a.name, RawMessage: "the agent reported " + code, Retryable: code == errTimeout}
	default:
		// Still queued or delivered when the budget ran out: the command may
		// land later, which is why this one is retryable.
		return nil, &provider.Error{Code: errTimeout, Provider: a.name, RawMessage: "the agent did not answer in time", Retryable: true}
	}
}

func decode[T any](raw json.RawMessage, out *T) error {
	if len(raw) == 0 {
		return errors.New("empty result")
	}
	return json.Unmarshal(raw, out)
}

// Health implements provider.Provider: the gateway is up whenever the store
// answers.
func (a *Adapter) Health(ctx context.Context) (*provider.Health, error) {
	if err := a.store.pool.Ping(ctx); err != nil {
		return nil, &provider.Error{Code: errUnreachable, Provider: a.name, RawMessage: err.Error()}
	}
	return &provider.Health{Status: "up"}, nil
}

// Capabilities implements provider.Provider: an agent manages the node it
// runs on, for both runtimes the platform schedules.
func (*Adapter) Capabilities(_ context.Context) (*provider.Capabilities, error) {
	return &provider.Capabilities{
		CreateInstance:    true,
		DeleteInstance:    true,
		Start:             true,
		Stop:              true,
		Restart:           true,
		Reinstall:         true,
		ResetPassword:     false,
		Traffic:           true,
		Metrics:           true,
		NAT:               true,
		IPv4:              true,
		IPv6:              true,
		SupportedRuntimes: []string{"kvm", "lxc"},
	}, nil
}

type listImagesResult struct {
	Images []provider.Image `json:"images"`
}

// ListImages implements provider.Provider; the agent is the source of its
// node's images, and the command carries no filter.
func (a *Adapter) ListImages(ctx context.Context, nodeID string) ([]provider.Image, error) {
	agentID, err := a.agentFor(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdListImages, "images:"+nodeID, map[string]any{})
	if err != nil {
		return nil, err
	}
	var result listImagesResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return result.Images, nil
}

type instanceStateResult struct {
	ProviderInstanceID string   `json:"provider_instance_id"`
	State              string   `json:"state"`
	IPv4               []string `json:"ipv4"`
	IPv6               []string `json:"ipv6"`
}

// GetInstance implements provider.Provider: the state report is one command.
func (a *Adapter) GetInstance(ctx context.Context, req provider.GetInstanceRequest) (*provider.Instance, error) {
	agentID, err := a.agentForInstance(ctx, req)
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdGetState,
		"state:"+req.ProviderInstanceID, map[string]any{"provider_instance_id": req.ProviderInstanceID})
	if err != nil {
		return nil, err
	}
	var result instanceStateResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return &provider.Instance{
		ProviderInstanceID: result.ProviderInstanceID,
		State:              result.State,
		IPv4:               result.IPv4,
		IPv6:               result.IPv6,
	}, nil
}

type createResult struct {
	ProviderInstanceID string `json:"provider_instance_id"`
	State              string `json:"state"`
}

// CreateInstance implements provider.Provider; idempotency is the key: a
// redelivered create is the same row, and the agent answers it as the same
// acceptance (ADR-013 §3).
func (a *Adapter) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Operation, error) {
	agentID, err := a.agentFor(ctx, req.NodeID)
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdCreateInstance, req.IdempotencyKey, map[string]any{
		"name":      req.Name,
		"cpu_cores": req.CPUCores,
		"memory_mb": req.MemoryMB,
		"disk_gb":   req.DiskGB,
		"image":     req.Image,
		"runtime":   req.Virtualization,
	})
	if err != nil {
		return nil, err
	}
	var result createResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	a.rememberNode(result.ProviderInstanceID, req.NodeID)
	return &provider.Operation{
		ProviderOperationID: result.ProviderInstanceID,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"instance_id": result.ProviderInstanceID},
	}, nil
}

// action builds the simple start/stop/restart/delete family. The node comes
// from the request when it names one, otherwise from the placement the
// create recorded.
func (a *Adapter) action(ctx context.Context, cmdType, nodeID, providerInstanceID, operationID, idempotencyKey string) (*provider.Operation, error) {
	agentID, err := a.agentForInstance(ctx, provider.GetInstanceRequest{
		NodeID:             nodeID,
		ProviderInstanceID: providerInstanceID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := a.dispatch(ctx, agentID, cmdType, idempotencyKey, map[string]any{
		"provider_instance_id": providerInstanceID,
		"operation_id":         operationID,
	}); err != nil {
		return nil, err
	}
	return &provider.Operation{ProviderOperationID: providerInstanceID, Status: "succeeded", Accepted: true}, nil
}

func (a *Adapter) StartInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.action(ctx, CmdStartInstance, req.NodeID, req.ProviderInstanceID, req.OperationID, "start:"+req.ProviderInstanceID+":"+req.IdempotencyKey)
}

func (a *Adapter) StopInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.action(ctx, CmdStopInstance, req.NodeID, req.ProviderInstanceID, req.OperationID, "stop:"+req.ProviderInstanceID+":"+req.IdempotencyKey)
}

func (a *Adapter) RestartInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.action(ctx, CmdRestartInstance, req.NodeID, req.ProviderInstanceID, req.OperationID, "restart:"+req.ProviderInstanceID+":"+req.IdempotencyKey)
}

func (a *Adapter) DeleteInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.action(ctx, CmdDeleteInstance, req.NodeID, req.ProviderInstanceID, req.OperationID, "delete:"+req.ProviderInstanceID+":"+req.IdempotencyKey)
}

func (a *Adapter) ReinstallInstance(ctx context.Context, req provider.ReinstallInstanceRequest) (*provider.Operation, error) {
	agentID, err := a.agentForInstance(ctx, provider.GetInstanceRequest{
		NodeID:             req.NodeID,
		ProviderInstanceID: req.ProviderInstanceID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := a.dispatch(ctx, agentID, CmdReinstallInstance,
		"reinstall:"+req.ProviderInstanceID+":"+req.Image+":"+req.IdempotencyKey,
		map[string]any{
			"provider_instance_id": req.ProviderInstanceID,
			"image":                req.Image,
		}); err != nil {
		return nil, err
	}
	return &provider.Operation{ProviderOperationID: req.ProviderInstanceID, Status: "succeeded", Accepted: true}, nil
}

// ResetPassword implements provider.Provider as the honest refusal: the
// agent channel could carry it, but V1's agent does not implement it (ADR-010
// made the same statement for the direct provider).
func (a *Adapter) ResetPassword(_ context.Context, _ provider.ResetPasswordRequest) (*provider.Operation, error) {
	return nil, &provider.Error{Code: "UNSUPPORTED_OPERATION", Provider: a.name, RawMessage: "the agent does not implement password reset"}
}

type usageResult struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  int64   `json:"memory_used_mb"`
	MemoryTotalMB int64   `json:"memory_total_mb"`
	DiskUsedGB    float64 `json:"disk_used_gb"`
	DiskTotalGB   float64 `json:"disk_total_gb"`
}

func (a *Adapter) GetUsage(ctx context.Context, req provider.GetInstanceRequest) (*provider.Usage, error) {
	agentID, err := a.agentForInstance(ctx, req)
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdGetUsage,
		"usage:"+req.ProviderInstanceID+":"+fmt.Sprint(time.Now().Unix()/60),
		map[string]any{"provider_instance_id": req.ProviderInstanceID})
	if err != nil {
		return nil, err
	}
	var result usageResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return &provider.Usage{
		CPUPercent:    result.CPUPercent,
		MemoryUsedMB:  result.MemoryUsedMB,
		MemoryTotalMB: result.MemoryTotalMB,
		DiskUsedGB:    result.DiskUsedGB,
		DiskTotalGB:   result.DiskTotalGB,
		ObservedAt:    time.Now().UTC(),
	}, nil
}

type trafficResult struct {
	RXBytes int64 `json:"rx_bytes"`
	TXBytes int64 `json:"tx_bytes"`
}

func (a *Adapter) GetTraffic(ctx context.Context, req provider.GetTrafficRequest) (*provider.Traffic, error) {
	agentID, err := a.agentFor(ctx, a.nodeFor(provider.GetInstanceRequest{ProviderInstanceID: req.ProviderInstanceID}))
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdGetTraffic,
		"traffic:"+req.ProviderInstanceID+":"+req.From.UTC().Format(time.RFC3339),
		map[string]any{"provider_instance_id": req.ProviderInstanceID, "from": req.From, "to": req.To})
	if err != nil {
		return nil, err
	}
	var result trafficResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return &provider.Traffic{RXBytes: result.RXBytes, TXBytes: result.TXBytes, From: req.From, To: req.To}, nil
}

type portForwardsResult struct {
	Forwards []provider.PortForward `json:"forwards"`
}

func (a *Adapter) ListPortForwards(ctx context.Context, req provider.GetInstanceRequest) ([]provider.PortForward, error) {
	agentID, err := a.agentForInstance(ctx, req)
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdListPortForwards,
		"forwards:"+req.ProviderInstanceID, map[string]any{"provider_instance_id": req.ProviderInstanceID})
	if err != nil {
		return nil, err
	}
	var result portForwardsResult
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return result.Forwards, nil
}

func (a *Adapter) AddPortForward(ctx context.Context, req provider.AddPortForwardRequest) (*provider.Operation, error) {
	agentID, err := a.agentForInstance(ctx, provider.GetInstanceRequest{ProviderInstanceID: req.ProviderInstanceID})
	if err != nil {
		return nil, err
	}
	raw, err := a.dispatch(ctx, agentID, CmdAddPortForward,
		"forward-add:"+req.ProviderInstanceID+":"+req.Protocol+":"+fmt.Sprint(req.PublicPort)+":"+req.IdempotencyKey,
		map[string]any{
			"provider_instance_id": req.ProviderInstanceID,
			"protocol":             req.Protocol,
			"public_port":          req.PublicPort,
			"guest_port":           req.GuestPort,
			"description":          req.Description,
		})
	if err != nil {
		return nil, err
	}
	var result struct {
		MappingID string `json:"mapping_id"`
	}
	if err := decode(raw, &result); err != nil {
		return nil, &provider.Error{Code: errRejected, Provider: a.name, RawMessage: err.Error()}
	}
	return &provider.Operation{
		ProviderOperationID: req.ProviderInstanceID,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"mapping_id": result.MappingID},
	}, nil
}

func (a *Adapter) DeletePortForward(ctx context.Context, req provider.DeletePortForwardRequest) (*provider.Operation, error) {
	agentID, err := a.agentFor(ctx, req.NodeID)
	if err != nil {
		return nil, err
	}
	if _, err := a.dispatch(ctx, agentID, CmdDeletePortForward,
		"forward-del:"+req.ProviderMappingID+":"+req.IdempotencyKey,
		map[string]any{"provider_mapping_id": req.ProviderMappingID}); err != nil {
		return nil, err
	}
	return &provider.Operation{ProviderOperationID: req.ProviderMappingID, Status: "succeeded", Accepted: true}, nil
}

package lxdapi

// The port's implementation over LXD's /1.0 surface (ADR-010).
//
// Every method here is a translation: LXD's async operations into the port's
// accepted create, LXD's instance states into the port's state words, LXD's
// proxy devices into the port's port forwards. What LXD cannot do is stated
// as UNSUPPORTED_OPERATION, not approximated.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// lxdState maps LXD's instance status words to the port's.
func lxdState(status string) string {
	switch status {
	case "Running":
		return "running"
	case "Stopped":
		return "stopped"
	case "Frozen":
		return "suspended"
	case "Error":
		return "error"
	default:
		// Starting, Stopping, Pending and friends are transitions; the port's
		// callers read states, not transitions, and pending is the honest word.
		return "pending"
	}
}

// Health implements provider.Provider: LXD's /1.0 carries the server identity.
func (a *Adapter) Health(ctx context.Context) (*provider.Health, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0", nil)
	if err != nil {
		return nil, err
	}
	meta := object(env)
	env2, _ := meta["env"].(map[string]any)
	version, _ := env2["server_version"].(string)
	health := &provider.Health{
		Status:    "up",
		Version:   version,
		CheckedAt: time.Now().UTC(),
	}
	return health, nil
}

// Capabilities implements provider.Provider: LXD can do everything the port
// names except resetting a password through the API alone.
func (*Adapter) Capabilities(context.Context) (*provider.Capabilities, error) {
	return &provider.Capabilities{
		CreateInstance:    true,
		DeleteInstance:    true,
		Start:             true,
		Stop:              true,
		Restart:           true,
		Reinstall:         true,
		ResetPassword:     false, // needs exec inside the machine; stated, not faked
		Traffic:           true,  // cumulative counters, per ADR-010's honesty note
		Metrics:           false,
		NAT:               true, // proxy devices
		IPv4:              true,
		IPv6:              true,
		Snapshot:          false,
		Console:           false,
		Firewall:          false,
		SupportedRuntimes: []string{"kvm", "lxc"},
	}, nil
}

// ListImages implements provider.Provider: LXD's image store, recursively.
func (a *Adapter) ListImages(ctx context.Context, _ string) ([]provider.Image, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0/images?recursion=1", nil)
	if err != nil {
		return nil, err
	}
	var raws []map[string]any
	if len(env.Metadata) > 0 {
		if err := json.Unmarshal(env.Metadata, &raws); err != nil {
			return nil, &provider.Error{
				Code: "NETWORK_ERROR", Provider: a.Name(),
				RawMessage: "decode the image list: " + err.Error(),
			}
		}
	}
	images := make([]provider.Image, 0, len(raws))
	for _, raw := range raws {
		fingerprint, _ := raw["fingerprint"].(string)
		properties, _ := raw["properties"].(map[string]any)
		os, _ := properties["os"].(string)
		release, _ := properties["release"].(string)
		description, _ := properties["description"].(string)
		arch, _ := raw["architecture"].(string)
		images = append(images, provider.Image{
			ID:          fingerprint,
			Name:        os + " " + release,
			OS:          os,
			Version:     release,
			Arch:        arch,
			Description: description,
		})
	}
	return images, nil
}

// CreateInstance implements provider.Provider. The idempotency key becomes
// the instance's LXD name, so a retried create meets the machine the first
// attempt made and is answered identically (ADR-010 §2).
func (a *Adapter) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Operation, error) {
	if req.IdempotencyKey == "" {
		return nil, &provider.Error{
			Code: "UNKNOWN_PROVIDER_ERROR", Provider: a.Name(),
			RawMessage: "CreateInstance requires an idempotency key",
		}
	}
	name := instanceName(req.IdempotencyKey)

	payload := map[string]any{
		"name": name,
		"source": map[string]any{
			"type":     "image",
			"alias":    req.Image,
			"protocol": "simplestreams",
			"server":   "https://images.linuxcontainers.org",
		},
		"instance_type": req.Virtualization,
		"config": map[string]any{
			"limits.cpu":             fmt.Sprintf("%g", req.CPUCores),
			"limits.memory":          fmt.Sprintf("%dMiB", req.MemoryMB),
			"security.root_password": orEmpty(req.RootPassword),
		},
	}
	// The disk size rides on the root device's size property.
	payload["devices"] = map[string]any{
		"root": map[string]any{
			"type": "disk",
			"path": "/",
			"pool": "default",
			"size": fmt.Sprintf("%dGiB", req.DiskGB),
		},
	}

	env, err := a.call(ctx, http.MethodPost, "/1.0/instances?target="+req.NodeID, payload)
	if err != nil {
		var perr *provider.Error
		if asProviderError(err, &perr) && perr.Code == "INSTANCE_ALREADY_EXISTS" {
			// The first attempt already made this machine. The retry is the
			// same create, answered the same way — same operation identifier,
			// same instance (ADR-010 §2).
			return &provider.Operation{
				ProviderOperationID: "lxd-create-" + name,
				Status:              "succeeded",
				Accepted:            true,
				Metadata:            map[string]any{"instance_id": name},
			}, nil
		}
		return nil, err
	}
	if err := a.wait(ctx, env); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "lxd-create-" + name,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"instance_id": name},
	}, nil
}

// GetInstance implements provider.Provider: the instance and its live state.
func (a *Adapter) GetInstance(ctx context.Context, req provider.GetInstanceRequest) (*provider.Instance, error) {
	name := req.ProviderInstanceID
	if name == "" {
		return nil, &provider.Error{
			Code: "INSTANCE_NOT_FOUND", Provider: a.Name(),
			RawMessage: "no provider instance identifier was given",
		}
	}
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+name, nil)
	if err != nil {
		return nil, err
	}
	meta := object(env)
	status, _ := meta["status"].(string)
	createdAt, _ := meta["created_at"].(string)

	stateEnv, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+name+"/state", nil)
	if err != nil {
		return nil, err
	}
	state := object(stateEnv)

	instance := &provider.Instance{
		ProviderInstanceID: name,
		State:              lxdState(status),
		CreatedAt:          parseTime(createdAt),
		Metadata:           map[string]any{"lxd_status": status},
	}
	if cpu, ok := state["cpu"].(map[string]any); ok {
		usage, _ := cpu["usage"].(float64)
		_ = usage
	}
	if mem, ok := state["memory"].(map[string]any); ok {
		total, _ := mem["total"].(float64)
		instance.MemoryMB = int64(total / (1 << 20))
	}
	if disk, ok := state["disk"].(map[string]any); ok {
		if root, ok := disk["root"].(map[string]any); ok {
			total, _ := root["total"].(float64)
			instance.DiskGB = int64(total / (1 << 30))
		}
	}
	for _, key := range []string{"eth0", "enp5s0"} {
		if networks, ok := state["network"].(map[string]any); ok {
			if nic, ok := networks[key].(map[string]any); ok {
				addresses, _ := nic["addresses"].([]any)
				for _, address := range addresses {
					addr, _ := address.(map[string]any)
					family, _ := addr["family"].(string)
					value, _ := addr["address"].(string)
					switch family {
					case "inet":
						instance.IPv4 = append(instance.IPv4, value)
					case "inet6":
						instance.IPv6 = append(instance.IPv6, value)
					}
				}
			}
		}
	}
	return instance, nil
}

// stateAction moves an instance's power state through LXD's state endpoint.
func (a *Adapter) stateAction(ctx context.Context, req provider.InstanceActionRequest, action string) (*provider.Operation, error) {
	env, err := a.call(ctx, http.MethodPut, "/1.0/instances/"+req.ProviderInstanceID+"/state",
		map[string]any{"action": action, "force": false, "timeout": 30})
	if err != nil {
		return nil, err
	}
	if err := a.wait(ctx, env); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "lxd-" + action + "-" + req.ProviderInstanceID,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"instance_id": req.ProviderInstanceID},
	}, nil
}

// StartInstance implements provider.Provider.
func (a *Adapter) StartInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.stateAction(ctx, req, "start")
}

// StopInstance implements provider.Provider.
func (a *Adapter) StopInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.stateAction(ctx, req, "stop")
}

// RestartInstance implements provider.Provider.
func (a *Adapter) RestartInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	return a.stateAction(ctx, req, "restart")
}

// ReinstallInstance implements provider.Provider: LXD has no reinstall verb,
// so the adapter composes one — delete the machine, create it again with the
// same name and the new image. The name's uniqueness is what makes the pair
// safe: nothing else can hold the name between the two calls.
func (a *Adapter) ReinstallInstance(ctx context.Context, req provider.ReinstallInstanceRequest) (*provider.Operation, error) {
	if _, err := a.DeleteInstance(ctx, req.InstanceActionRequest); err != nil {
		return nil, err
	}
	return a.CreateInstance(ctx, provider.CreateInstanceRequest{
		OperationID:    req.OperationID,
		IdempotencyKey: req.IdempotencyKey,
		NodeID:         req.NodeID,
		InstanceID:     req.IdempotencyKey,
		Name:           req.ProviderInstanceID,
		Image:          req.Image,
		RootPassword:   req.RootPassword,
		Virtualization: "kvm",
	})
}

// ResetPassword implements provider.Provider: LXD's API cannot set a root
// password without executing inside the machine, so the contract's honest
// answer is the refusal docs/06 named for exactly this.
func (*Adapter) ResetPassword(context.Context, provider.ResetPasswordRequest) (*provider.Operation, error) {
	return nil, &provider.Error{
		Code: "UNSUPPORTED_OPERATION", Provider: "lxd",
		RawMessage: "LXD's API cannot reset a root password without in-guest execution",
	}
}

// DeleteInstance implements provider.Provider: the machine goes away, and a
// second delete meets 404 — which the contract accepts as the idempotent end.
func (a *Adapter) DeleteInstance(ctx context.Context, req provider.InstanceActionRequest) (*provider.Operation, error) {
	env, err := a.call(ctx, http.MethodDelete, "/1.0/instances/"+req.ProviderInstanceID, nil)
	if err != nil {
		var perr *provider.Error
		if asProviderError(err, &perr) && perr.Code == "INSTANCE_NOT_FOUND" {
			// Already gone: the delete is idempotent, and the contract allows
			// either this answer or a plain success. The answer is the same
			// shape either way, so callers need no second branch.
			return &provider.Operation{
				ProviderOperationID: "lxd-delete-" + req.ProviderInstanceID,
				Status:              "succeeded",
				Accepted:            true,
				Metadata:            map[string]any{"instance_id": req.ProviderInstanceID},
			}, nil
		}
		return nil, err
	}
	if err := a.wait(ctx, env); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "lxd-delete-" + req.ProviderInstanceID,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"instance_id": req.ProviderInstanceID},
	}, nil
}

// GetUsage implements provider.Provider: LXD's live state carries the
// counters the port's usage wants.
func (a *Adapter) GetUsage(ctx context.Context, req provider.GetInstanceRequest) (*provider.Usage, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+req.ProviderInstanceID+"/state", nil)
	if err != nil {
		return nil, err
	}
	state := object(env)
	usage := &provider.Usage{ObservedAt: time.Now().UTC()}
	if cpu, ok := state["cpu"].(map[string]any); ok {
		if seconds, ok := cpu["usage"].(float64); ok {
			// LXD reports cumulative CPU seconds; the port wants a percentage,
			// which needs two samples. One sample is the honest placeholder:
			// zero claims nothing, a fabricated ratio would claim everything.
			_ = seconds
			usage.CPUPercent = 0
		}
	}
	if mem, ok := state["memory"].(map[string]any); ok {
		used, _ := mem["usage"].(float64)
		total, _ := mem["total"].(float64)
		usage.MemoryUsedMB = int64(used / (1 << 20))
		usage.MemoryTotalMB = int64(total / (1 << 20))
	}
	if disk, ok := state["disk"].(map[string]any); ok {
		if root, ok := disk["root"].(map[string]any); ok {
			used, _ := root["usage"].(float64)
			total, _ := root["total"].(float64)
			usage.DiskUsedGB = used / (1 << 30)
			usage.DiskTotalGB = total / (1 << 30)
		}
	}
	return usage, nil
}

// GetTraffic implements provider.Provider: LXD's NIC counters are cumulative
// since boot, so the window the port carries is echoed honestly and the
// totals are what LXD knows (ADR-010's note on Traffic).
func (a *Adapter) GetTraffic(ctx context.Context, req provider.GetTrafficRequest) (*provider.Traffic, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+req.ProviderInstanceID+"/state", nil)
	if err != nil {
		return nil, err
	}
	state := object(env)
	traffic := &provider.Traffic{From: req.From, To: req.To}
	if networks, ok := state["network"].(map[string]any); ok {
		for _, name := range []string{"eth0", "enp5s0"} {
			nic, ok := networks[name].(map[string]any)
			if !ok {
				continue
			}
			counters, _ := nic["counters"].(map[string]any)
			received, _ := counters["bytes_received"].(float64)
			sent, _ := counters["bytes_sent"].(float64)
			traffic.RXBytes += int64(received)
			traffic.TXBytes += int64(sent)
		}
	}
	return traffic, nil
}

// ListPortForwards implements provider.Provider: the instance's proxy devices.
func (a *Adapter) ListPortForwards(ctx context.Context, req provider.GetInstanceRequest) ([]provider.PortForward, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+req.ProviderInstanceID, nil)
	if err != nil {
		return nil, err
	}
	meta := object(env)
	devices, _ := meta["devices"].(map[string]any)
	forwards := make([]provider.PortForward, 0, len(devices))
	for deviceName, raw := range devices {
		device, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		deviceType, _ := device["type"].(string)
		if deviceType != "proxy" {
			continue
		}
		forwards = append(forwards, decodeProxyDevice(deviceName, device))
	}
	return forwards, nil
}

// AddPortForward implements provider.Provider: one proxy device, named after
// its mapping, patched onto the instance.
func (a *Adapter) AddPortForward(ctx context.Context, req provider.AddPortForwardRequest) (*provider.Operation, error) {
	deviceName := proxyDeviceName(req.Protocol, req.PublicPort)
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+req.ProviderInstanceID, nil)
	if err != nil {
		return nil, err
	}
	meta := object(env)
	devices, _ := meta["devices"].(map[string]any)
	devices[deviceName] = map[string]any{
		"type":    "proxy",
		"listen":  fmt.Sprintf("%s:0.0.0.0:%d", req.Protocol, req.PublicPort),
		"connect": fmt.Sprintf("tcp:127.0.0.1:%d", req.GuestPort),
	}
	if _, err := a.call(ctx, http.MethodPatch, "/1.0/instances/"+req.ProviderInstanceID,
		map[string]any{"devices": devices}); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "lxd-pf-" + deviceName,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"mapping_id": deviceName},
	}, nil
}

// DeletePortForward implements provider.Provider: the named device goes away.
func (a *Adapter) DeletePortForward(ctx context.Context, req provider.DeletePortForwardRequest) (*provider.Operation, error) {
	env, err := a.call(ctx, http.MethodGet, "/1.0/instances/"+req.ProviderInstanceID, nil)
	if err != nil {
		return nil, err
	}
	meta := object(env)
	devices, _ := meta["devices"].(map[string]any)
	delete(devices, req.ProviderMappingID)
	if _, err := a.call(ctx, http.MethodPatch, "/1.0/instances/"+req.ProviderInstanceID,
		map[string]any{"devices": devices}); err != nil {
		return nil, err
	}
	return &provider.Operation{
		ProviderOperationID: "lxd-pf-del-" + req.ProviderMappingID,
		Status:              "succeeded",
		Accepted:            true,
		Metadata:            map[string]any{"mapping_id": req.ProviderMappingID},
	}, nil
}

// proxyDeviceName is the deterministic device name a mapping rides on.
func proxyDeviceName(protocol string, publicPort int) string {
	return "pf-" + protocol + "-" + fmt.Sprintf("%d", publicPort)
}

// decodeProxyDevice reads a LXD proxy device back into the port's shape.
func decodeProxyDevice(name string, device map[string]any) provider.PortForward {
	listen, _ := device["listen"].(string)
	connect, _ := device["connect"].(string)
	protocol := "tcp"
	if hasPrefix(listen, "udp:") {
		protocol = "udp"
	} else if hasPrefix(listen, "tcp:") {
		protocol = "tcp"
	}
	publicPort, guestPort := 0, 0
	if after, found := portAfterLastColon(listen); found {
		_, _ = fmt.Sscanf(after, "0.0.0.0:%d", &publicPort)
	}
	if after, found := portAfterLastColon(connect); found {
		_, _ = fmt.Sscanf(after, "127.0.0.1:%d", &guestPort)
	}
	return provider.PortForward{
		ProviderMappingID: name,
		Protocol:          protocol,
		PublicPort:        publicPort,
		GuestPort:         guestPort,
	}
}

func hasPrefix(value, prefix string) bool {
	return strings.HasPrefix(value, prefix)
}

// portAfterLastColon reads the port off a host:port shape, where the host
// itself may carry colons (IPv6).
func portAfterLastColon(value string) (port string, found bool) {
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", false
	}
	return value[index+1:], true
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func asProviderError(err error, target **provider.Error) bool {
	return errors.As(err, target)
}

func orEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

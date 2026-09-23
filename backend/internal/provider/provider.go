package provider

import (
	"context"
	"time"
)

type Capabilities struct {
	CreateInstance    bool
	DeleteInstance    bool
	Start             bool
	Stop              bool
	Restart           bool
	Reinstall         bool
	ResetPassword     bool
	Traffic           bool
	Metrics           bool
	NAT               bool
	IPv4              bool
	IPv6              bool
	Snapshot          bool
	Console           bool
	Firewall          bool
	SupportedRuntimes []string
}

type Health struct {
	Status    string
	Version   string
	CheckedAt time.Time
	Details   map[string]any
}

type CreateInstanceRequest struct {
	OperationID    string
	IdempotencyKey string
	NodeID         string
	InstanceID     string
	Name           string
	CPUCores       float64
	MemoryMB       int64
	DiskGB         int64
	TrafficGB      *int64
	BandwidthMbps  *int
	IPv4Count      int
	IPv6Count      int
	NATPortCount   int
	Image          string
	Virtualization string
	RootPassword   *string
}

type InstanceActionRequest struct {
	OperationID        string
	IdempotencyKey     string
	NodeID             string
	ProviderInstanceID string
}

type GetInstanceRequest struct {
	NodeID             string
	ProviderInstanceID string
	PlatformInstanceID string
}

type ReinstallInstanceRequest struct {
	InstanceActionRequest
	Image        string
	RootPassword *string
}

type ResetPasswordRequest struct {
	InstanceActionRequest
	RootPassword string
}

type GetTrafficRequest struct {
	GetInstanceRequest
	From time.Time
	To   time.Time
}

type AddPortForwardRequest struct {
	InstanceActionRequest
	Protocol    string
	PublicIP    string
	PublicPort  int
	GuestPort   int
	Description string
}

type DeletePortForwardRequest struct {
	InstanceActionRequest
	ProviderMappingID string
}

type Operation struct {
	ProviderOperationID string
	Status              string
	Accepted            bool
	Metadata            map[string]any
}

type Instance struct {
	ProviderInstanceID string
	State              string
	CPUCores           float64
	MemoryMB           int64
	DiskGB             int64
	IPv4               []string
	IPv6               []string
	CreatedAt          time.Time
	Metadata           map[string]any
}

type Image struct {
	ID          string
	Name        string
	OS          string
	Version     string
	Arch        string
	Description string
}

type Usage struct {
	CPUPercent    float64
	MemoryUsedMB  int64
	MemoryTotalMB int64
	DiskUsedGB    float64
	DiskTotalGB   float64
	ObservedAt    time.Time
}

type Traffic struct {
	RXBytes int64
	TXBytes int64
	From    time.Time
	To      time.Time
}

type PortForward struct {
	ProviderMappingID string
	Protocol          string
	PublicIP          string
	PublicPort        int
	GuestPort         int
	Description       string
}

type Error struct {
	Code       string
	Retryable  bool
	Provider   string
	RawCode    string
	RawMessage string
	Cause      error
}

func (e *Error) Error() string {
	if e.RawMessage != "" {
		return e.Code + ": " + e.RawMessage
	}
	return e.Code
}

type Provider interface {
	Name() string
	Health(context.Context) (*Health, error)
	Capabilities(context.Context) (*Capabilities, error)
	ListImages(ctx context.Context, nodeID string) ([]Image, error)

	CreateInstance(context.Context, CreateInstanceRequest) (*Operation, error)
	GetInstance(context.Context, GetInstanceRequest) (*Instance, error)
	StartInstance(context.Context, InstanceActionRequest) (*Operation, error)
	StopInstance(context.Context, InstanceActionRequest) (*Operation, error)
	RestartInstance(context.Context, InstanceActionRequest) (*Operation, error)
	ReinstallInstance(context.Context, ReinstallInstanceRequest) (*Operation, error)
	ResetPassword(context.Context, ResetPasswordRequest) (*Operation, error)
	DeleteInstance(context.Context, InstanceActionRequest) (*Operation, error)

	GetUsage(context.Context, GetInstanceRequest) (*Usage, error)
	GetTraffic(context.Context, GetTrafficRequest) (*Traffic, error)

	ListPortForwards(context.Context, GetInstanceRequest) ([]PortForward, error)
	AddPortForward(context.Context, AddPortForwardRequest) (*Operation, error)
	DeletePortForward(context.Context, DeletePortForwardRequest) (*Operation, error)
}

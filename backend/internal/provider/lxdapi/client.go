// Package lxdapi is the direct provider adapter for LXD's REST API (ADR-010).
//
// It depends on internal/provider alone: the business core is on the other
// side of the port, and this package does not know it exists. Everything LXD
// — its envelope, its async operations, its proxy devices, its error codes —
// is translated here, once, so the rest of the platform never sees a provider.
package lxdapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// Config is the adapter's connection and behaviour.
type Config struct {
	// Endpoint is the LXD API's base URL, e.g. https://lxd.example.com:8443.
	Endpoint string
	// Token authenticates every request; LXD's trust-token deployment.
	Token string
	// Timeout bounds one request and one operation wait. Zero means the
	// default of thirty seconds.
	Timeout time.Duration
	// Client replaces the transport in tests.
	Client *http.Client
}

// DefaultTimeout is one request's patience.
const DefaultTimeout = 30 * time.Second

// Adapter is the LXD-backed implementation of the provider port.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New builds the adapter over a config.
func New(cfg Config) *Adapter {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Adapter{cfg: cfg, client: client}
}

// Name implements provider.Provider.
func (*Adapter) Name() string { return "lxd" }

// the LXD envelope: every response carries one.
type envelope struct {
	Type       string          `json:"type"` // sync | async | error
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Operation  string          `json:"operation"`
	Metadata   json.RawMessage `json:"metadata"`
	Err        string          `json:"error"`
	ErrorCode  int             `json:"error_code"`
}

// call performs one request and decodes the envelope. The error it returns on
// an LXD-level failure is the contract's own type, already mapped.
func (a *Adapter) call(ctx context.Context, method, path string, body any) (*envelope, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &provider.Error{
				Code: "UNKNOWN_PROVIDER_ERROR", Provider: a.Name(),
				RawMessage: "encode the request: " + err.Error(),
			}
		}
		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.Endpoint, "/")+path, reader)
	if err != nil {
		return nil, &provider.Error{
			Code: "UNKNOWN_PROVIDER_ERROR", Provider: a.Name(),
			RawMessage: "build the request: " + err.Error(),
		}
	}
	request.Header.Set("Content-Type", "application/json")
	if a.cfg.Token != "" {
		request.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return nil, a.transportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	envelopeBytes, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, a.transportError(err)
	}

	var env envelope
	if err := json.Unmarshal(envelopeBytes, &env); err != nil {
		// A non-JSON body from something that claims to be LXD: a proxy's error
		// page, a truncated response. The status code is all that is known.
		if response.StatusCode >= 400 {
			return nil, a.httpError(response.StatusCode, "")
		}
		return nil, &provider.Error{
			Code: "NETWORK_ERROR", Provider: a.Name(),
			RawMessage: fmt.Sprintf("unreadable response (status %d)", response.StatusCode),
		}
	}
	if env.Type == "error" || env.ErrorCode >= 400 || response.StatusCode >= 400 {
		return nil, a.apiError(&env, path)
	}
	return &env, nil
}

// wait blocks until the async operation LXD returned has settled, bounded by
// the adapter's timeout. LXD's /wait endpoint does the polling server-side.
// The settled envelope carries nothing the callers use — only whether the
// wait succeeded — so the answer is just the error.
func (a *Adapter) wait(ctx context.Context, env *envelope) error {
	if env.Type != "async" || env.Operation == "" {
		return nil // a sync answer needs no wait
	}
	waitCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()
	_, err := a.call(waitCtx, http.MethodGet, env.Operation+"/wait?timeout="+
		fmt.Sprintf("%d", int(a.cfg.Timeout.Seconds())), nil)
	return err
}

// transportError maps what the HTTP layer itself reported.
func (a *Adapter) transportError(err error) error {
	var perr *provider.Error
	if errors.As(err, &perr) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		isTimeout(err) {
		return &provider.Error{
			Code: "PROVIDER_TIMEOUT", Retryable: true, Provider: a.Name(),
			RawMessage: err.Error(), Cause: err,
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return &provider.Error{
			Code: "PROVIDER_UNAVAILABLE", Retryable: true, Provider: a.Name(),
			RawMessage: err.Error(), Cause: err,
		}
	}
	return &provider.Error{
		Code: "NETWORK_ERROR", Provider: a.Name(),
		RawMessage: err.Error(), Cause: err,
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// apiError maps LXD's own error envelope into the contract's vocabulary.
//
// The 404 is message-driven, not blanket: LXD answers 404 for a missing
// instance, a missing image and a missing operation alike, and only the first
// is the contract's INSTANCE_NOT_FOUND — a wait for an operation that vanished
// is a transport problem, not a machine that does not exist.
func (a *Adapter) apiError(env *envelope, path string) error {
	perr := &provider.Error{
		Provider:   a.Name(),
		RawCode:    fmt.Sprintf("%d", env.ErrorCode),
		RawMessage: env.Err,
	}
	message := strings.ToLower(env.Err)
	switch {
	case env.ErrorCode == 401 || env.ErrorCode == 403:
		perr.Code = "PROVIDER_AUTH_FAILED"
	case env.ErrorCode == 404 && strings.Contains(message, "instance"):
		perr.Code = "INSTANCE_NOT_FOUND"
	case env.ErrorCode == 404 && strings.HasPrefix(path, "/1.0/images"):
		perr.Code = "IMAGE_NOT_FOUND"
	case strings.Contains(message, "already exists"):
		perr.Code = "INSTANCE_ALREADY_EXISTS"
	case env.ErrorCode >= 500:
		perr.Code = "PROVIDER_UNAVAILABLE"
		perr.Retryable = true
	default:
		perr.Code = "NETWORK_ERROR"
		perr.Retryable = env.ErrorCode == 404
	}
	return perr
}

// httpError builds the contract's error from a status code when the body
// carried no readable envelope.
func (a *Adapter) httpError(status int, message string) error {
	perr := &provider.Error{
		Provider:   a.Name(),
		RawCode:    fmt.Sprintf("%d", status),
		RawMessage: message,
	}
	switch {
	case status == 401 || status == 403:
		perr.Code = "PROVIDER_AUTH_FAILED"
	case status == 404:
		perr.Code = "INSTANCE_NOT_FOUND"
	case status >= 500:
		perr.Code = "PROVIDER_UNAVAILABLE"
		perr.Retryable = true
	default:
		perr.Code = "UNKNOWN_PROVIDER_ERROR"
	}
	return perr
}

// instanceName derives the LXD instance name from the platform's idempotency
// key — deterministically, so a retry meets the same machine (ADR-010 §2).
// LXD names are DNS-safe: the hash sidesteps every character rule at once.
func instanceName(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "vps-" + hex.EncodeToString(sum[:8])
}

// metadata decodes an envelope's metadata into a JSON object.
func object(env *envelope) map[string]any {
	var decoded map[string]any
	_ = json.Unmarshal(env.Metadata, &decoded)
	return decoded
}

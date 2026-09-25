package lxdapi_test

// The contract suite against the LXD adapter, run against a fake LXD server
// (internal/provider/lxdapi/lxdtest) that speaks the real API's shapes —
// envelope, async operations, error codes — so the adapter's transport,
// parsing, mapping and idempotency are all exercised without a hypervisor.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/contracttest"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/lxdapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/lxdapi/lxdtest"
)

// The suite Phase 4 wrote against the mock is the suite the LXD adapter
// passes — the Gate's "replace the mock without touching the business core"
// begins with the contract being provider-agnostic.
func TestLXDAdapterSatisfiesTheProviderContract(t *testing.T) {
	lxdtest.NewServer(t)
	contracttest.RunContractTests(t, func(t *testing.T) provider.Provider {
		return newAdapter(t)
	})
}

func TestTheAdapterMapsTheErrorsTheContractNames(t *testing.T) {
	newFakeLXD(t)
	adapter := newAdapter(t)
	ctx := context.Background()

	// A ghost instance is the contract's own not-found, not a generic 404.
	_, err := adapter.GetInstance(ctx, provider.GetInstanceRequest{ProviderInstanceID: "ghost"})
	assertCode(t, err, "INSTANCE_NOT_FOUND")

	// A wrong credential is the contract's auth failure.
	bad := lxdapi.New(lxdapi.Config{Endpoint: fakeEndpoint(t), Token: "wrong", Timeout: 5 * time.Second})
	_, err = bad.Health(ctx)
	assertCode(t, err, "PROVIDER_AUTH_FAILED")

	// What LXD cannot do at all is stated, not faked.
	_, err = adapter.ResetPassword(ctx, provider.ResetPasswordRequest{})
	assertCode(t, err, "UNSUPPORTED_OPERATION")
}

func TestAProviderTimeoutIsTheContractSOwnError(t *testing.T) {
	// A server that outlasts the client's patience: the transport error is
	// mapped to PROVIDER_TIMEOUT with the retryable flag the engine's backoff
	// reads.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{}}`))
	}))
	t.Cleanup(slow.Close)

	adapter := lxdapi.New(lxdapi.Config{Endpoint: slow.URL, Token: "x", Timeout: 50 * time.Millisecond})
	_, err := adapter.Health(context.Background())
	assertCode(t, err, "PROVIDER_TIMEOUT")
}

func newFakeLXD(t *testing.T) *lxdtest.FakeLXD {
	t.Helper()
	fake, _ := lxdtest.NewServer(t)
	return fake
}

func newAdapter(t *testing.T) provider.Provider {
	t.Helper()
	_, endpoint := lxdtest.NewServer(t)
	return lxdapi.New(lxdapi.Config{
		Endpoint: endpoint,
		Token:    "test-token",
		Timeout:  10 * time.Second,
	})
}

func fakeEndpoint(t *testing.T) string {
	t.Helper()
	_, endpoint := lxdtest.NewServer(t)
	return endpoint
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a provider error with code %s, got none", code)
	}
	var perr *provider.Error
	if !errorsAs(err, &perr) {
		t.Fatalf("expected a *provider.Error, got %T: %v", err, err)
	}
	if perr.Code != code {
		t.Fatalf("expected code %s, got %s (%s)", code, perr.Code, perr.RawCode)
	}
}

func errorsAs(err error, target **provider.Error) bool {
	return errors.As(err, target)
}

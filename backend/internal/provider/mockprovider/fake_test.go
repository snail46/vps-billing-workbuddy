package mockprovider_test

import (
	"testing"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/contracttest"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/mockprovider"
)

// The mock is the contract suite's first tenant: if it cannot pass, the suite is
// wrong; when a direct provider cannot pass the same suite, the provider is.
func TestFakeSatisfiesTheProviderContract(t *testing.T) {
	contracttest.RunContractTests(t, func(t *testing.T) provider.Provider {
		return mockprovider.New("mock-test")
	})
}

package runman_test

// The runman adapter against the provider contract (ADR-013 §5): the suite
// the mock and the direct provider passed, driven by a fake agent that walks
// the gateway's state machine in-process. One placement of the fake agent per
// test process; the claim loop is transactional, so extra loops are harmless.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	contracttest "github.com/snail46/vps-billing-workbuddy/backend/internal/provider/contracttest"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/runman"
)

const contractNodeID = "node-1"

var (
	fixtureOnce sync.Once
	fixtureErr  error
	testAdapter *runman.Adapter
)

func newFixture(t *testing.T) provider.Provider {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	fixtureOnce.Do(func() {
		// The pool lives for the whole test process: the suite calls the
		// factory once per subtest, and a cleanup bound to the first subtest
		// would close the pool under the others' feet.
		pool, err := pgxpool.New(context.Background(), databaseURL)
		if err != nil {
			fixtureErr = fmt.Errorf("connect: %w", err)
			return
		}
		store := runman.New(pool)

		// The contract suite addresses "node-1"; replace whatever enrollment a
		// previous run left behind. The commands' rows are the agent's history,
		// and a fresh agent is a fresh history.
		if _, err := pool.Exec(context.Background(), "DELETE FROM runman_commands WHERE agent_id IN (SELECT id FROM runman_agents WHERE node_id = $1)", contractNodeID); err != nil {
			fixtureErr = fmt.Errorf("clear commands: %w", err)
			return
		}
		if _, err := pool.Exec(context.Background(), "DELETE FROM runman_agents WHERE node_id = $1", contractNodeID); err != nil {
			fixtureErr = fmt.Errorf("clear agents: %w", err)
			return
		}
		if _, err := store.CreateAgent(context.Background(), contractNodeID, "contract-token"); err != nil {
			fixtureErr = fmt.Errorf("enroll agent: %w", err)
			return
		}
		agent, err := store.Authenticate(context.Background(), "contract-token")
		if err != nil {
			fixtureErr = fmt.Errorf("read agent: %w", err)
			return
		}

		go fakeAgent(store, agent.ID)

		testAdapter = runman.Build(store, "runman")
	})
	if fixtureErr != nil {
		t.Fatalf("fixture: %v", fixtureErr)
	}
	return testAdapter
}

// fakeAgent is the node's side of the gateway: claim, answer, repeat. It
// keeps the instance set an agent would hold — created instances answer,
// unknown ones fail with the contract's own code.
func fakeAgent(store *runman.Store, agentID uuid.UUID) {
	ctx := context.Background()
	instances := map[string]bool{}
	mapping := 0
	for {
		commands, err := store.Claim(ctx, agentID)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		for _, command := range commands {
			complete := func(status, errorCode string, result map[string]any) {
				encoded, _ := json.Marshal(result)
				_ = store.Complete(ctx, command.ID, status, encoded, errorCode)
			}
			var payload struct {
				ProviderInstanceID string `json:"provider_instance_id"`
				Name               string `json:"name"`
				Image              string `json:"image"`
			}
			_ = json.Unmarshal(command.Payload, &payload)

			switch command.Type {
			case "list_images":
				complete("succeeded", "", map[string]any{"images": []map[string]any{
					{"id": "debian-12", "os": "debian", "version": "12", "arch": "amd64"},
				}})
			case "create_instance":
				id := "ri-" + payload.Name
				instances[id] = true
				complete("succeeded", "", map[string]any{"provider_instance_id": id, "state": "running"})
			case "get_state":
				if !instances[payload.ProviderInstanceID] {
					complete("failed", "INSTANCE_NOT_FOUND", nil)
					continue
				}
				complete("succeeded", "", map[string]any{
					"provider_instance_id": payload.ProviderInstanceID, "state": "running",
					"ipv4": []string{"192.0.2.10"},
				})
			case "delete_instance":
				delete(instances, payload.ProviderInstanceID)
				complete("succeeded", "", nil)
			case "stop_instance", "start_instance", "restart_instance", "reinstall_instance":
				if command.Type != "reinstall_instance" && !instances[payload.ProviderInstanceID] {
					complete("failed", "INSTANCE_NOT_FOUND", nil)
					continue
				}
				complete("succeeded", "", nil)
			case "get_usage":
				complete("succeeded", "", map[string]any{
					"cpu_percent": 3.5, "memory_used_mb": 128, "memory_total_mb": 1024,
					"disk_used_gb": 2.0, "disk_total_gb": 20.0,
				})
			case "get_traffic":
				complete("succeeded", "", map[string]any{"rx_bytes": 1024, "tx_bytes": 2048})
			case "add_port_forward":
				mapping++
				complete("succeeded", "", map[string]any{"mapping_id": fmt.Sprintf("map-%d", mapping)})
			case "list_port_forwards":
				complete("succeeded", "", map[string]any{"forwards": []map[string]any{}})
			case "delete_port_forward":
				complete("succeeded", "", nil)
			default:
				complete("failed", "UNKNOWN_COMMAND", nil)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheRunmanAdapterPassesTheContract(t *testing.T) {
	contracttest.RunContractTests(t, newFixture)
}

// The gateway's own surface: authentication refuses a wrong token, and a
// heartbeat stamps the agent. The state machine between claim and result is
// what the contract test above drives through the store.
func TestTheGatewayRefusesABadToken(t *testing.T) {
	newFixture(t)
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	store := runman.New(pool)
	if _, err := store.Authenticate(context.Background(), "no-such-token"); err == nil {
		t.Fatal("an unknown token authenticated")
	}
	if _, err := store.Heartbeat(context.Background(), uuid.New()); err == nil {
		t.Fatal("an unknown agent heartbeated")
	}
	_ = slog.Default()
}

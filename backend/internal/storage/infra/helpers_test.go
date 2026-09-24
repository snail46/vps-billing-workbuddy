package infrastore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("postgres is not reachable: %v", err)
	}
	return pool
}

func testNow() time.Time { return time.Now().UTC() }

func findNode(t *testing.T, nodes []infra.Node, id uuid.UUID) infra.Node {
	t.Helper()
	for i := range nodes {
		if nodes[i].ID == id {
			return nodes[i]
		}
	}
	t.Fatalf("node %s is not in the listing", id)
	return infra.Node{}
}

var _ = uuid.New

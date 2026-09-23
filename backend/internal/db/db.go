// Package db owns the PostgreSQL connection pool lifecycle.
//
// Only connection management lives here. Statements belong to the phase that
// owns the domain they query, so this package never knows about a table.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultConnectTimeout guards against a zero value producing a ping with no
// deadline, which would hang startup indefinitely against a black-holed host.
const defaultConnectTimeout = 5 * time.Second

// Options configures the connection pool.
type Options struct {
	URL               string
	MaxConns          int32
	MinConns          int32
	ConnectTimeout    time.Duration
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// NewPool creates the pool and verifies connectivity.
//
// pgxpool connects lazily, so a wrong DATABASE_URL would otherwise surface on
// the first request rather than at startup. Pinging here makes a misconfigured
// deployment fail immediately and legibly, which is also what lets the readiness
// probe mean something from the very first moment the process is up.
func NewPool(ctx context.Context, opts Options) (*pgxpool.Pool, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("db: URL is required")
	}

	poolCfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("db: parse DATABASE_URL: %w", err)
	}

	connectTimeout := opts.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}

	poolCfg.MaxConns = opts.MaxConns
	poolCfg.MinConns = opts.MinConns
	poolCfg.MaxConnLifetime = opts.MaxConnLifetime
	poolCfg.MaxConnIdleTime = opts.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = opts.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return pool, nil
}

// Checker reports PostgreSQL reachability to the readiness probe.
type Checker struct {
	pool *pgxpool.Pool
}

// NewChecker wraps a pool as a readiness check.
func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Name identifies the dependency in the readiness report.
func (*Checker) Name() string { return "postgres" }

// Check pings PostgreSQL. The caller supplies the deadline.
func (c *Checker) Check(ctx context.Context) error {
	if c.pool == nil {
		return fmt.Errorf("db: pool is not initialised")
	}
	return c.pool.Ping(ctx)
}

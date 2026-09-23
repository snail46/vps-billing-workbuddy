// Package redisx owns the Redis client lifecycle.
//
// Redis backs the queue, cache and session concerns introduced by later phases.
// Phase 0 establishes the connection and its readiness probe only, so that the
// foundation can report accurately on a dependency the platform will require.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultDialTimeout = 5 * time.Second

// Options configures the Redis client.
type Options struct {
	URL          string
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// NewClient parses the URL, creates the client and verifies connectivity.
//
// The go-redis client connects lazily, so an unreachable Redis would not be
// noticed until first use. Pinging at startup converts that into an immediate,
// legible failure — and makes the readiness probe honest.
func NewClient(ctx context.Context, opts Options) (*redis.Client, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("redisx: URL is required")
	}

	redisOpts, err := redis.ParseURL(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("redisx: parse REDIS_URL: %w", err)
	}
	redisOpts.DialTimeout = opts.DialTimeout
	redisOpts.ReadTimeout = opts.ReadTimeout
	redisOpts.WriteTimeout = opts.WriteTimeout

	client := redis.NewClient(redisOpts)

	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultDialTimeout
	}

	pingCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: ping: %w", err)
	}

	return client, nil
}

// Checker reports Redis reachability to the readiness probe.
type Checker struct {
	client *redis.Client
}

// NewChecker wraps a client as a readiness check.
func NewChecker(client *redis.Client) *Checker { return &Checker{client: client} }

// Name identifies the dependency in the readiness report.
func (*Checker) Name() string { return "redis" }

// Check pings Redis. The caller supplies the deadline.
func (c *Checker) Check(ctx context.Context) error {
	if c.client == nil {
		return fmt.Errorf("redisx: client is not initialised")
	}
	return c.client.Ping(ctx).Err()
}

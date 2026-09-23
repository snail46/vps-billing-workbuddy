package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AttemptCounter counts authentication attempts in Redis.
//
// It implements the counter the authentication middleware needs. The window is set on
// every increment rather than only when the key is created. That makes the budget mean
// "this many attempts, then this much quiet" instead of "this many attempts per fixed
// window", which is the behaviour worth having for throttling: a caller who keeps trying
// stays blocked, and the block lifts once they stop.
//
// Setting the expiry unconditionally also removes an ordering trap. Incrementing first
// and expiring only when the count is 1 would mean a process that died between the two
// left a key with no expiry, which is a permanent lockout rather than a throttled one.
// Both commands are issued in one transaction, so they cannot be separated at all.
type AttemptCounter struct {
	client *redis.Client
}

// NewAttemptCounter builds a counter.
func NewAttemptCounter(client *redis.Client) *AttemptCounter {
	return &AttemptCounter{client: client}
}

// Incr increments a key and returns its new value.
func (c *AttemptCounter) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	pipe := c.client.TxPipeline()
	count := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("redisx: count attempt: %w", err)
	}
	return count.Val(), nil
}

// Reset removes keys.
func (c *AttemptCounter) Reset(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redisx: reset attempt counters: %w", err)
	}
	return nil
}

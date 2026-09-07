package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"github.com/devstroop/notalk/internal/config"
)

// Cache is an opt-in Redis cache. When disabled it no-ops (miss on Get, no-op on Set).
type Cache struct {
	enabled bool
	client  *redis.Client
}

// New creates a Cache from config. If cfg.Redis.Enabled is false, it returns a disabled no-op cache.
func New(cfg config.RedisConfig) *Cache {
	if !cfg.Enabled {
		log.Info().Msg("cache: redis disabled (opt-in, set NOTALK_REDIS_ENABLED=true)")
		return &Cache{enabled: false}
	}
	addr := cfg.Addr
	if addr == "" {
		addr = "redis:6379"
	}
	opt := &redis.Options{
		Addr:     addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	client := redis.NewClient(opt)
	// quick ping with timeout; if fails, fall back to disabled but keep client for retry
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Str("addr", addr).Msg("cache: redis ping failed, cache will retry on next op")
	}
	log.Info().Str("addr", addr).Int("db", cfg.DB).Msg("cache: redis enabled")
	return &Cache{enabled: true, client: client}
}

// Enabled reports whether Redis is enabled.
func (c *Cache) Enabled() bool { return c.enabled && c.client != nil }

// Ping checks Redis connectivity.
func (c *Cache) Ping(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Ping(ctx).Err()
}

// Get returns string value or redis.Nil if miss/disabled.
func (c *Cache) Get(ctx context.Context, key string) (string, error) {
	if !c.Enabled() {
		return "", redis.Nil
	}
	return c.client.Get(ctx, key).Result()
}

// Set stores a value with TTL (0 = no expiry).
func (c *Cache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Set(ctx, key, value, ttl).Err()
}

// Del removes keys.
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if !c.Enabled() || len(keys) == 0 {
		return nil
	}
	return c.client.Del(ctx, keys...).Err()
}

// GetJSON fetches JSON into dst; returns redis.Nil on miss/disabled.
func (c *Cache) GetJSON(ctx context.Context, key string, dst interface{}) error {
	if !c.Enabled() {
		return redis.Nil
	}
	s, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), dst)
}

// SetJSON stores value as JSON with TTL.
func (c *Cache) SetJSON(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if !c.Enabled() {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, b, ttl).Err()
}

// FlushDB clears current DB (admin).
func (c *Cache) FlushDB(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.FlushDB(ctx).Err()
}

// Close closes the client.
func (c *Cache) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Keys returns a simple prefix scan (for overlay feedback — count keys).
func (c *Cache) Keys(ctx context.Context, pattern string) ([]string, error) {
	if !c.Enabled() {
		return nil, nil
	}
	return c.client.Keys(ctx, pattern).Result()
}

package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
)

// validConfig returns a configuration that passes validation, so each test can
// change exactly one field and attribute the failure to it.
func validConfig() config.Config {
	return config.Config{
		AppEnv:              config.EnvDevelopment,
		AppBaseURL:          "http://localhost:8080",
		HTTPAddr:            ":8080",
		HTTPReadTimeout:     15 * time.Second,
		HTTPWriteTimeout:    30 * time.Second,
		HTTPIdleTimeout:     60 * time.Second,
		HTTPShutdownTimeout: 20 * time.Second,
		HTTPRequestTimeout:  20 * time.Second,
		UserWebOrigin:       "http://localhost:3000",
		AdminWebOrigin:      "http://localhost:3001",
		DatabaseURL:         "postgres://vps_billing:secret@postgres:5432/vps_billing?sslmode=disable",
		DBMaxConns:          10,
		DBMinConns:          0,
		DBConnectTimeout:    5 * time.Second,
		DBMaxConnLifetime:   time.Hour,
		DBMaxConnIdleTime:   30 * time.Minute,
		DBHealthCheckPeriod: time.Minute,
		RedisURL:            "redis://redis:6379/0",
		RedisDialTimeout:    5 * time.Second,
		RedisReadTimeout:    3 * time.Second,
		RedisWriteTimeout:   3 * time.Second,
		LogLevel:            "info",
		LogFormat:           "json",
		WorkerTickInterval:  30 * time.Second,
	}
}

func TestValidateAcceptsValidConfiguration(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("expected a valid configuration, got error: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantSub string
	}{
		{
			name:    "unknown environment",
			mutate:  func(c *config.Config) { c.AppEnv = "prd" },
			wantSub: "APP_ENV",
		},
		{
			name:    "malformed database url",
			mutate:  func(c *config.Config) { c.DatabaseURL = "postgres://user:pass@host:port/db\n" },
			wantSub: "DATABASE_URL",
		},
		{
			name:    "wrong database scheme",
			mutate:  func(c *config.Config) { c.DatabaseURL = "mysql://user:pass@host:3306/db" },
			wantSub: "DATABASE_URL",
		},
		{
			name:    "redis scheme not allowed",
			mutate:  func(c *config.Config) { c.RedisURL = "http://redis:6379" },
			wantSub: "REDIS_URL",
		},
		{
			name:    "empty http address",
			mutate:  func(c *config.Config) { c.HTTPAddr = "   " },
			wantSub: "HTTP_ADDR",
		},
		{
			name:    "origin without host",
			mutate:  func(c *config.Config) { c.UserWebOrigin = "http://" },
			wantSub: "USER_WEB_ORIGIN",
		},
		{
			name:    "origin with unsupported scheme",
			mutate:  func(c *config.Config) { c.AdminWebOrigin = "ftp://admin.example.com" },
			wantSub: "ADMIN_WEB_ORIGIN",
		},
		{
			name:    "db max conns below one",
			mutate:  func(c *config.Config) { c.DBMaxConns = 0 },
			wantSub: "DB_MAX_CONNS",
		},
		{
			name: "db min conns above max",
			mutate: func(c *config.Config) {
				c.DBMaxConns = 4
				c.DBMinConns = 5
			},
			wantSub: "DB_MIN_CONNS",
		},
		{
			name:    "non positive shutdown timeout",
			mutate:  func(c *config.Config) { c.HTTPShutdownTimeout = 0 },
			wantSub: "HTTP_SHUTDOWN_TIMEOUT",
		},
		{
			name:    "non positive worker tick",
			mutate:  func(c *config.Config) { c.WorkerTickInterval = 0 },
			wantSub: "WORKER_TICK_INTERVAL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation to fail for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error mentioning %q, got %q", tc.wantSub, err.Error())
			}
		})
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	// t.Setenv restores the previous value automatically, and makes the test
	// independent of whatever the developer's shell happens to export.
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")

	// An empty env var is indistinguishable from an absent one for the required
	// tag, so the loader must reject it rather than produce a zero value.
	if _, err := config.Load(); err == nil {
		t.Fatal("expected Load to fail when DATABASE_URL is empty")
	}
}

func TestLoadAppliesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://vps_billing:secret@localhost:5432/vps_billing?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("APP_ENV", config.EnvStaging)
	t.Setenv("HTTP_ADDR", ":9090")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AppEnv != config.EnvStaging {
		t.Fatalf("expected APP_ENV to be overridden, got %q", cfg.AppEnv)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Fatalf("expected HTTP_ADDR to be overridden, got %q", cfg.HTTPAddr)
	}
	// A default must survive when the variable is unset.
	if cfg.LogFormat != "json" {
		t.Fatalf("expected default LOG_FORMAT json, got %q", cfg.LogFormat)
	}
	if cfg.IsProduction() {
		t.Fatal("staging must not report as production")
	}
}

func TestAllowedOriginsSkipsBlanks(t *testing.T) {
	cfg := validConfig()
	cfg.UserWebOrigin = " http://localhost:3000 "
	cfg.AdminWebOrigin = "   "

	origins := cfg.AllowedOrigins()
	if len(origins) != 1 || origins[0] != "http://localhost:3000" {
		t.Fatalf("expected the single trimmed origin, got %#v", origins)
	}
}

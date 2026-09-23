// Package config loads and validates process configuration from the
// environment.
//
// Only settings that Phase 0 actually consumes are modelled. Adding a knob that
// nothing reads would create configuration that looks meaningful but silently
// does nothing, so settings are introduced by the phase that needs them.
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment names accepted by APP_ENV.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config is the fully resolved process configuration.
type Config struct {
	AppEnv     string `env:"APP_ENV" envDefault:"development"`
	AppBaseURL string `env:"APP_BASE_URL" envDefault:"http://localhost:8080"`

	HTTPAddr            string        `env:"HTTP_ADDR" envDefault:":8080"`
	HTTPReadTimeout     time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"15s"`
	HTTPWriteTimeout    time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"30s"`
	HTTPIdleTimeout     time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`
	HTTPShutdownTimeout time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"20s"`
	// HTTPRequestTimeout is the per-request processing deadline. It is kept
	// below HTTP_WRITE_TIMEOUT so that a request that runs out of time is
	// answered with a documented error envelope rather than having its
	// connection cut by the server mid-write.
	HTTPRequestTimeout time.Duration `env:"HTTP_REQUEST_TIMEOUT" envDefault:"20s"`

	// Browser origins permitted by CORS. docs/14 requires a production
	// allowlist rather than a wildcard.
	UserWebOrigin  string `env:"USER_WEB_ORIGIN" envDefault:"http://localhost:3000"`
	AdminWebOrigin string `env:"ADMIN_WEB_ORIGIN" envDefault:"http://localhost:3001"`

	// SessionCookieSecure forces the Secure attribute on the session cookies.
	//
	// Left unset it follows APP_ENV, which is what makes the default right in both
	// places: production is always Secure, while development over plain HTTP works
	// without every developer having to remember a flag. A pointer rather than a bool
	// because "unset" and "explicitly false" have to be distinguishable — the second
	// is a deliberate, and dangerous, choice.
	SessionCookieSecure *bool `env:"SESSION_COOKIE_SECURE"`

	// Authentication attempt budgets, per client address and per submitted account.
	//
	// The address budget is the larger of the two because one address is routinely a
	// university, an office or a mobile carrier gateway behind which many unrelated
	// people sign in.
	RateLimitLoginPerIP      int           `env:"RATE_LIMIT_LOGIN_PER_IP" envDefault:"20"`
	RateLimitLoginPerAccount int           `env:"RATE_LIMIT_LOGIN_PER_ACCOUNT" envDefault:"5"`
	RateLimitRegisterPerIP   int           `env:"RATE_LIMIT_REGISTER_PER_IP" envDefault:"10"`
	RateLimitWindow          time.Duration `env:"RATE_LIMIT_WINDOW" envDefault:"15m"`

	// PaymentFakeSecret signs the fake gateway's callbacks. It is what the webhook
	// endpoint verifies against, so a deployment that keeps the development value is a
	// deployment whose payments can be forged by anyone who has read this repository.
	PaymentFakeSecret string `env:"PAYMENT_FAKE_SECRET" envDefault:"development-fake-gateway-secret"`

	DatabaseURL         string        `env:"DATABASE_URL,required"`
	DBMaxConns          int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	DBMinConns          int32         `env:"DB_MIN_CONNS" envDefault:"0"`
	DBConnectTimeout    time.Duration `env:"DB_CONNECT_TIMEOUT" envDefault:"5s"`
	DBMaxConnLifetime   time.Duration `env:"DB_MAX_CONN_LIFETIME" envDefault:"1h"`
	DBMaxConnIdleTime   time.Duration `env:"DB_MAX_CONN_IDLE_TIME" envDefault:"30m"`
	DBHealthCheckPeriod time.Duration `env:"DB_HEALTH_CHECK_PERIOD" envDefault:"1m"`

	RedisURL          string        `env:"REDIS_URL" envDefault:"redis://127.0.0.1:6379/0"`
	RedisDialTimeout  time.Duration `env:"REDIS_DIAL_TIMEOUT" envDefault:"5s"`
	RedisReadTimeout  time.Duration `env:"REDIS_READ_TIMEOUT" envDefault:"3s"`
	RedisWriteTimeout time.Duration `env:"REDIS_WRITE_TIMEOUT" envDefault:"3s"`

	LogLevel  string `env:"LOG_LEVEL" envDefault:"info"`
	LogFormat string `env:"LOG_FORMAT" envDefault:"json"`

	// WorkerTickInterval controls how often the worker process verifies its
	// dependencies while idle. It is not a business cadence: scheduled work is
	// added by the phase that owns it.
	WorkerTickInterval time.Duration `env:"WORKER_TICK_INTERVAL" envDefault:"30s"`
}

// Load reads configuration from the process environment and validates it.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: read environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration for internal consistency.
//
// Validation is intentionally strict about anything that would otherwise fail
// later and less legibly — a malformed DATABASE_URL surfaces at startup with a
// clear message instead of as a connection error under load.
func (c Config) Validate() error {
	if err := c.validateEnv(); err != nil {
		return err
	}

	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("config: HTTP_ADDR must not be empty")
	}

	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
	}
	if err := validateURLScheme("DATABASE_URL", c.DatabaseURL, "postgres", "postgresql"); err != nil {
		return err
	}

	if strings.TrimSpace(c.RedisURL) == "" {
		return fmt.Errorf("config: REDIS_URL must not be empty")
	}
	if err := validateURLScheme("REDIS_URL", c.RedisURL, "redis", "rediss"); err != nil {
		return err
	}

	if err := validateOrigin("USER_WEB_ORIGIN", c.UserWebOrigin); err != nil {
		return err
	}
	if err := validateOrigin("ADMIN_WEB_ORIGIN", c.AdminWebOrigin); err != nil {
		return err
	}

	if c.DBMaxConns < 1 {
		return fmt.Errorf("config: DB_MAX_CONNS must be at least 1, got %d", c.DBMaxConns)
	}
	if c.DBMinConns < 0 || c.DBMinConns > c.DBMaxConns {
		return fmt.Errorf("config: DB_MIN_CONNS must be within [0, DB_MAX_CONNS], got %d with max %d", c.DBMinConns, c.DBMaxConns)
	}

	if c.HTTPShutdownTimeout <= 0 {
		return fmt.Errorf("config: HTTP_SHUTDOWN_TIMEOUT must be positive, got %s", c.HTTPShutdownTimeout)
	}
	if c.WorkerTickInterval <= 0 {
		return fmt.Errorf("config: WORKER_TICK_INTERVAL must be positive, got %s", c.WorkerTickInterval)
	}

	for field, value := range map[string]int{
		"RATE_LIMIT_LOGIN_PER_IP":      c.RateLimitLoginPerIP,
		"RATE_LIMIT_LOGIN_PER_ACCOUNT": c.RateLimitLoginPerAccount,
		"RATE_LIMIT_REGISTER_PER_IP":   c.RateLimitRegisterPerIP,
	} {
		if value < 1 {
			return fmt.Errorf("config: %s must be at least 1, got %d", field, value)
		}
	}
	if c.RateLimitWindow <= 0 {
		return fmt.Errorf("config: RATE_LIMIT_WINDOW must be positive, got %s", c.RateLimitWindow)
	}

	if c.IsProduction() && c.SessionCookieSecure != nil && !*c.SessionCookieSecure {
		return fmt.Errorf("config: SESSION_COOKIE_SECURE=false is not permitted when APP_ENV is %s", EnvProduction)
	}
	if c.IsProduction() && c.PaymentFakeSecret == "development-fake-gateway-secret" {
		return fmt.Errorf("config: PAYMENT_FAKE_SECRET must be changed when APP_ENV is %s", EnvProduction)
	}

	return nil
}

// SecureCookies reports whether session cookies carry the Secure attribute.
//
// Production is refused a false value rather than merely defaulting to true: a
// session cookie sent over plain HTTP is readable by anyone on the path, so an
// explicit opt-out in production is a configuration error, not a preference.
func (c Config) SecureCookies() bool {
	if c.IsProduction() {
		return true
	}
	return c.SessionCookieSecure != nil && *c.SessionCookieSecure
}

func (c Config) validateEnv() error {
	switch c.AppEnv {
	case EnvDevelopment, EnvStaging, EnvProduction:
		return nil
	default:
		return fmt.Errorf("config: APP_ENV %q is not one of %s, %s, %s",
			c.AppEnv, EnvDevelopment, EnvStaging, EnvProduction)
	}
}

// IsProduction reports whether the process runs in the production environment.
func (c Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// AllowedOrigins returns the browser origins permitted by CORS.
func (c Config) AllowedOrigins() []string {
	origins := make([]string, 0, 2)
	for _, o := range []string{c.UserWebOrigin, c.AdminWebOrigin} {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

func validateURLScheme(field, raw string, allowed ...string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: %s is not a valid URL: %w", field, err)
	}
	for _, scheme := range allowed {
		if u.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("config: %s must use one of the schemes %s, got %q", field, strings.Join(allowed, ", "), u.Scheme)
}

func validateOrigin(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: %s is not a valid origin: %w", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: %s must be an http or https origin, got %q", field, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("config: %s must include a host", field)
	}
	return nil
}

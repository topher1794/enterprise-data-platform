// Package config loads and validates all runtime configuration for the EDP
// control plane. Values are resolved from, in order of increasing precedence:
// built-in defaults, a YAML file, and EDP_-prefixed environment variables.
package config

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

// Config is the fully resolved application configuration.
type Config struct {
	App      AppConfig      `mapstructure:"app"`
	HTTP     HTTPConfig     `mapstructure:"http"`
	Database DatabaseConfig `mapstructure:"database"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Airflow  AirflowConfig  `mapstructure:"airflow"`
	Log      LogConfig      `mapstructure:"log"`
}

// AppConfig holds process-level metadata.
type AppConfig struct {
	Name     string `mapstructure:"name" validate:"required"`
	Env      string `mapstructure:"env" validate:"required,oneof=dev staging prod"`
	Version  string `mapstructure:"version"`
	BasePath string `mapstructure:"base_path"`
}

// IsProduction reports whether stricter production behaviour should apply.
func (a AppConfig) IsProduction() bool { return a.Env == "prod" }

// HTTPConfig controls the public listener and its timeouts.
type HTTPConfig struct {
	Host            string        `mapstructure:"host" validate:"required"`
	Port            int           `mapstructure:"port" validate:"required,min=1,max=65535"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout" validate:"required"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout" validate:"required"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout" validate:"required"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout" validate:"required"`
	CORSAllowOrigin []string      `mapstructure:"cors_allow_origins"`
}

// Addr is the listener address, suitable for net.Listen.
func (h HTTPConfig) Addr() string { return fmt.Sprintf("%s:%d", h.Host, h.Port) }

// DatabaseConfig describes the PostgreSQL connection and pooling behaviour.
type DatabaseConfig struct {
	Host            string        `mapstructure:"host" validate:"required"`
	Port            int           `mapstructure:"port" validate:"required,min=1,max=65535"`
	User            string        `mapstructure:"user" validate:"required"`
	Password        string        `mapstructure:"password" validate:"required"`
	Name            string        `mapstructure:"name" validate:"required"`
	SSLMode         string        `mapstructure:"ssl_mode" validate:"required,oneof=disable allow prefer require verify-ca verify-full"`
	MaxConns        int32         `mapstructure:"max_conns" validate:"required,min=1"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	MaxConnIdleTime time.Duration `mapstructure:"max_conn_idle_time"`
	HealthTimeout   time.Duration `mapstructure:"health_timeout" validate:"required"`
	MigrateOnBoot   bool          `mapstructure:"migrate_on_boot"`
	MigrationsPath  string        `mapstructure:"migrations_path"`
}

// DSN returns a libpq-style connection URL. The password is embedded, so this
// value must never be logged.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// RedactedDSN is the log-safe form of DSN, with the password elided.
func (d DatabaseConfig) RedactedDSN() string {
	return fmt.Sprintf(
		"postgres://%s:***@%s:%d/%s?sslmode=%s",
		d.User, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// AuthConfig controls authentication and authorization.
type AuthConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Issuer      string        `mapstructure:"issuer"`
	Audience    string        `mapstructure:"audience"`
	JWKSURL     string        `mapstructure:"jwks_url"`
	JWKSRefresh time.Duration `mapstructure:"jwks_refresh_interval"`
	ClockSkew   time.Duration `mapstructure:"clock_skew"`
	RolesClaim  string        `mapstructure:"roles_claim"`
	ScopesClaim string        `mapstructure:"scopes_claim"`
	TenantClaim string        `mapstructure:"tenant_claim"`
	AdminRoles  []string      `mapstructure:"admin_roles"`
}

// IsAdminRole reports whether role is configured as an administrative role.
func (a AuthConfig) IsAdminRole(role string) bool {
	for _, r := range a.AdminRoles {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}

// AirflowConfig points at the Airflow REST API used to trigger DAG runs.
type AirflowConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	BaseURL     string        `mapstructure:"base_url" validate:"required_if=Airflow.Enabled true"`
	Username    string        `mapstructure:"username"`
	Password    string        `mapstructure:"password"`
	Timeout     time.Duration `mapstructure:"timeout" validate:"required"`
	DAGIDPrefix string        `mapstructure:"dag_id_prefix"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level     string `mapstructure:"level" validate:"required,oneof=debug info warn error"`
	Format    string `mapstructure:"format" validate:"required,oneof=json text"`
	AddSource bool   `mapstructure:"add_source"`
}

// Load resolves configuration for the given config file path and environment.
// An empty path skips file loading and relies on defaults plus environment.
// The returned config is guaranteed to be valid.
func Load(path, env string) (*Config, error) {
	v := viper.New()

	setDefaults(v)
	bindEnv(v, env)

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}

	if cfg.App.BasePath == "" {
		cfg.App.BasePath = "/api/v1"
	}
	cfg.App.BasePath = "/" + strings.Trim(cfg.App.BasePath, "/")

	if cfg.Auth.RolesClaim == "" {
		cfg.Auth.RolesClaim = "roles"
	}
	if cfg.Auth.ScopesClaim == "" {
		cfg.Auth.ScopesClaim = "scope"
	}
	if cfg.Auth.TenantClaim == "" {
		cfg.Auth.TenantClaim = "tenant_id"
	}
	if cfg.Auth.ClockSkew == 0 {
		cfg.Auth.ClockSkew = 60 * time.Second
	}
	if cfg.Auth.JWKSRefresh == 0 {
		cfg.Auth.JWKSRefresh = 15 * time.Minute
	}
	if cfg.Auth.JWKSURL == "" && cfg.Auth.Issuer != "" {
		cfg.Auth.JWKSURL = strings.TrimSuffix(cfg.Auth.Issuer, "/") + "/.well-known/jwks.json"
	}

	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate enforces the `validate` struct tags and the cross-field invariants
// that tags cannot express.
func Validate(cfg *Config) error {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("mapstructure"), ",", 2)[0]
		if name == "" {
			return fld.Name
		}
		return name
	})

	if err := v.Struct(cfg); err != nil {
		var invalid *validator.InvalidValidationError
		if errors.As(err, &invalid) {
			return fmt.Errorf("configuration validation failed: %w", err)
		}

		errs, ok := err.(validator.ValidationErrors)
		if !ok {
			return fmt.Errorf("invalid configuration: %w", err)
		}

		var problems []string
		for _, fe := range errs {
			problems = append(problems, fmt.Sprintf("%s: %s", fe.Field(), fe.Tag()))
		}
		sort.Strings(problems)
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}

	if cfg.Database.MinConns > cfg.Database.MaxConns {
		return fmt.Errorf("invalid configuration: database.min_conns (%d) must not exceed database.max_conns (%d)",
			cfg.Database.MinConns, cfg.Database.MaxConns)
	}

	if cfg.HTTP.WriteTimeout < cfg.HTTP.ReadTimeout {
		return fmt.Errorf("invalid configuration: http.write_timeout must be >= http.read_timeout")
	}

	if cfg.Auth.Enabled {
		switch {
		case cfg.Auth.Issuer == "":
			return fmt.Errorf("invalid configuration: auth.issuer is required when auth.enabled is true")
		case cfg.Auth.JWKSURL == "":
			return fmt.Errorf("invalid configuration: auth.jwks_url is required when auth.enabled is true")
		}
	}
	return nil
}

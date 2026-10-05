package config

import (
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix for every environment variable that may override a
// configuration value, e.g. EDP_DATABASE__HOST.
const EnvPrefix = "EDP"

// KeyDelimiter separates nested segments in environment variable names.
const KeyDelimiter = "__"

// setDefaults registers the built-in defaults. These are the values the
// process falls back to when neither a config file nor the environment
// supplies an override.
func setDefaults(v *viper.Viper) {
	v.SetDefault("app.name", "edp-control-plane")
	v.SetDefault("app.env", "dev")
	v.SetDefault("app.version", "0.1.0")
	v.SetDefault("app.base_path", "/api/v1")

	v.SetDefault("http.host", "0.0.0.0")
	v.SetDefault("http.port", 8080)
	v.SetDefault("http.read_timeout", 15*time.Second)
	v.SetDefault("http.write_timeout", 30*time.Second)
	v.SetDefault("http.idle_timeout", 120*time.Second)
	v.SetDefault("http.shutdown_timeout", 20*time.Second)
	v.SetDefault("http.cors_allow_origins", []string{})

	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "edp")
	v.SetDefault("database.password", "edp")
	v.SetDefault("database.name", "edp_control_plane")
	v.SetDefault("database.ssl_mode", "disable")
	v.SetDefault("database.max_conns", 25)
	v.SetDefault("database.min_conns", 2)
	v.SetDefault("database.max_conn_lifetime", 30*time.Minute)
	v.SetDefault("database.max_conn_idle_time", 5*time.Minute)
	v.SetDefault("database.health_timeout", 3*time.Second)
	v.SetDefault("database.migrate_on_boot", false)
	v.SetDefault("database.migrations_path", "migrations")

	v.SetDefault("auth.enabled", false)
	v.SetDefault("auth.issuer", "")
	v.SetDefault("auth.audience", "edp-control-plane")
	v.SetDefault("auth.jwks_url", "")
	v.SetDefault("auth.jwks_refresh_interval", 15*time.Minute)
	v.SetDefault("auth.clock_skew", 60*time.Second)
	v.SetDefault("auth.roles_claim", "roles")
	v.SetDefault("auth.scopes_claim", "scope")
	v.SetDefault("auth.tenant_claim", "tenant_id")
	v.SetDefault("auth.admin_roles", []string{"platform-admin"})

	v.SetDefault("airflow.enabled", false)
	v.SetDefault("airflow.base_url", "http://localhost:8081")
	v.SetDefault("airflow.username", "")
	v.SetDefault("airflow.password", "")
	v.SetDefault("airflow.timeout", 10*time.Second)
	v.SetDefault("airflow.dag_id_prefix", "edp_")

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.add_source", false)
}

// bindEnv registers EDP_-prefixed environment variables and the automatic
// ENV__EDP_ENV indirection for locating the config file.
func bindEnv(v *viper.Viper, env string) {
	v.SetEnvPrefix(EnvPrefix)
	// Viper builds an environment variable name from a dotted key such as
	// `http.port` and then upper-cases it. The replacer therefore has to turn
	// the dot into the delimiter, which is what makes EDP_HTTP__PORT resolve.
	// Mapping the delimiter back to a dot would search for EDP_HTTP.PORT and
	// silently ignore every override.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", KeyDelimiter))
	v.AutomaticEnv()

	// ENV=prod becomes EDP_ENV=prod.
	_ = v.BindEnv("app.env", EnvPrefix+"_ENV")
	// CONFIG_PATH points at an alternative YAML file.
	_ = v.BindEnv("config.path", EnvPrefix+"_CONFIG_PATH")

	if env == "" {
		env = lookupEnv(EnvPrefix + "_ENV")
	}
	v.SetDefault("config.path", defaultConfigPath(env))
}

// defaultConfigPath resolves the conventional config file location for the
// named environment.
func defaultConfigPath(env string) string {
	if env == "" {
		env = "dev"
	}
	return "configs/config." + strings.ToLower(env) + ".yaml"
}

func lookupEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// ResolveConfigPath returns the config file to load: an explicit path if given,
// otherwise EDP_CONFIG_PATH, otherwise the conventional per-environment file.
func ResolveConfigPath(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if fromEnv := lookupEnv(EnvPrefix + "_CONFIG_PATH"); fromEnv != "" {
		return fromEnv
	}
	env := lookupEnv(EnvPrefix + "_ENV")
	if env == "" {
		env = "dev"
	}
	return defaultConfigPath(env)
}

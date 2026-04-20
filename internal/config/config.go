package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultPort                = 3000
	DefaultReplicationStreamID = "default"
	DefaultDBPoolSize          = 20
	DefaultCacheMaxAge         = 60
	DefaultCacheStaleAge       = 300
	DefaultLongPollTimeoutMS   = 20_000
	DefaultSSETimeoutMS        = 60_000
)

type StorageMode string

const (
	StorageModeMemory StorageMode = "memory"
	StorageModeDisk   StorageMode = "disk"
)

type MaxConcurrentRequests struct {
	Initial  int `json:"initial"`
	Existing int `json:"existing"`
}

type FeatureFlags []string

const (
	FeatureAllowSubqueries  = "allow_subqueries"
	FeatureTaggedSubqueries = "tagged_subqueries"
)

type CacheConfig struct {
	MaxAge   int
	StaleAge int
}

type StorageConfig struct {
	Mode StorageMode
	Dir  string
}

type TelemetryConfig struct {
	MetricsPath string
}

type Config struct {
	DatabaseURL           string
	PooledDatabaseURL     string
	Secret                string
	Insecure              bool
	Port                  int
	ListenHost            string
	ReplicationStreamID   string
	DBPoolSize            int
	MaxConcurrentRequests MaxConcurrentRequests
	Cache                 CacheConfig
	Storage               StorageConfig
	Telemetry             TelemetryConfig
	LongPollTimeoutMS     int
	SSETimeoutMS          int
	AllowShapeDeletion    bool
	FeatureFlags          FeatureFlags
}

func (f FeatureFlags) Enabled(flag string) bool {
	for _, candidate := range f {
		if candidate == flag {
			return true
		}
	}
	return false
}

type LookupEnvFunc func(string) (string, bool)

type Loader struct {
	LookupEnv LookupEnvFunc
}

func DefaultConfig() Config {
	return Config{
		Port:                DefaultPort,
		ReplicationStreamID: DefaultReplicationStreamID,
		DBPoolSize:          DefaultDBPoolSize,
		MaxConcurrentRequests: MaxConcurrentRequests{
			Initial:  300,
			Existing: 10000,
		},
		Cache: CacheConfig{
			MaxAge:   DefaultCacheMaxAge,
			StaleAge: DefaultCacheStaleAge,
		},
		Storage: StorageConfig{
			Mode: StorageModeMemory,
		},
		Telemetry: TelemetryConfig{
			MetricsPath: "/metrics",
		},
		LongPollTimeoutMS:  DefaultLongPollTimeoutMS,
		SSETimeoutMS:       DefaultSSETimeoutMS,
		AllowShapeDeletion: false,
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("DATABASE_URL is required")
	}

	if _, err := url.Parse(c.DatabaseURL); err != nil {
		return fmt.Errorf("invalid DATABASE_URL: %w", err)
	}

	if c.PooledDatabaseURL != "" {
		if _, err := url.Parse(c.PooledDatabaseURL); err != nil {
			return fmt.Errorf("invalid SYNC_POOLED_DATABASE_URL: %w", err)
		}
	}

	if !c.Insecure && strings.TrimSpace(c.Secret) == "" {
		return errors.New("SYNC_SECRET is required unless SYNC_INSECURE=true")
	}

	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid SYNC_PORT: %d", c.Port)
	}

	if c.DBPoolSize <= 0 {
		return fmt.Errorf("invalid SYNC_DB_POOL_SIZE: %d", c.DBPoolSize)
	}

	if c.MaxConcurrentRequests.Initial <= 0 || c.MaxConcurrentRequests.Existing <= 0 {
		return fmt.Errorf("invalid SYNC_MAX_CONCURRENT_REQUESTS: %+v", c.MaxConcurrentRequests)
	}

	if c.Cache.MaxAge < 0 || c.Cache.StaleAge < 0 {
		return fmt.Errorf("invalid cache config: %+v", c.Cache)
	}

	if c.LongPollTimeoutMS <= 0 {
		return fmt.Errorf("invalid long poll timeout: %d", c.LongPollTimeoutMS)
	}

	if c.SSETimeoutMS <= 0 {
		return fmt.Errorf("invalid sse timeout: %d", c.SSETimeoutMS)
	}

	if c.Storage.Mode == "" {
		c.Storage.Mode = StorageModeMemory
	}
	if c.Storage.Mode != StorageModeMemory && c.Storage.Mode != StorageModeDisk {
		return fmt.Errorf("invalid storage mode: %s", c.Storage.Mode)
	}

	return nil
}

func (c Config) ListenAddress() string {
	if strings.TrimSpace(c.ListenHost) == "" {
		return fmt.Sprintf(":%d", c.Port)
	}

	return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port))
}

func (l Loader) Load() (Config, error) {
	cfg := DefaultConfig()

	lookup := l.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	if value, ok := lookup("DATABASE_URL"); ok {
		cfg.DatabaseURL = strings.TrimSpace(value)
	}

	if value, ok := lookup("SYNC_POOLED_DATABASE_URL"); ok && strings.TrimSpace(value) != "" {
		cfg.PooledDatabaseURL = strings.TrimSpace(value)
	}

	if cfg.PooledDatabaseURL == "" {
		cfg.PooledDatabaseURL = cfg.DatabaseURL
	}

	if value, ok := lookup("SYNC_SECRET"); ok {
		cfg.Secret = strings.TrimSpace(value)
	}

	if value, ok := lookup("SYNC_INSECURE"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_INSECURE: %w", err)
		}
		cfg.Insecure = parsed
	}

	if value, ok := lookup("SYNC_PORT"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_PORT: %w", err)
		}
		cfg.Port = parsed
	}

	if value, ok := lookup("SYNC_REPLICATION_STREAM_ID"); ok && strings.TrimSpace(value) != "" {
		cfg.ReplicationStreamID = strings.TrimSpace(value)
	}

	if value, ok := lookup("SYNC_DB_POOL_SIZE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_DB_POOL_SIZE: %w", err)
		}
		cfg.DBPoolSize = parsed
	}

	if value, ok := lookup("SYNC_CACHE_MAX_AGE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_CACHE_MAX_AGE: %w", err)
		}
		cfg.Cache.MaxAge = parsed
	}

	if value, ok := lookup("SYNC_CACHE_STALE_AGE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_CACHE_STALE_AGE: %w", err)
		}
		cfg.Cache.StaleAge = parsed
	}

	if value, ok := lookup("SYNC_MAX_CONCURRENT_REQUESTS"); ok && strings.TrimSpace(value) != "" {
		if err := json.Unmarshal([]byte(value), &cfg.MaxConcurrentRequests); err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_MAX_CONCURRENT_REQUESTS: %w", err)
		}
	}

	if value, ok := lookup("SYNC_STORAGE_MODE"); ok && strings.TrimSpace(value) != "" {
		cfg.Storage.Mode = StorageMode(strings.TrimSpace(value))
	}

	if value, ok := lookup("SYNC_STORAGE_DIR"); ok {
		cfg.Storage.Dir = strings.TrimSpace(value)
	}

	if value, ok := lookup("SYNC_LONG_POLL_TIMEOUT_MS"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_LONG_POLL_TIMEOUT_MS: %w", err)
		}
		cfg.LongPollTimeoutMS = parsed
	}

	if value, ok := lookup("SYNC_SSE_TIMEOUT_MS"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_SSE_TIMEOUT_MS: %w", err)
		}
		cfg.SSETimeoutMS = parsed
	}

	if value, ok := lookup("SYNC_ALLOW_SHAPE_DELETION"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid SYNC_ALLOW_SHAPE_DELETION: %w", err)
		}
		cfg.AllowShapeDeletion = parsed
	}

	if value, ok := lookup("SYNC_FEATURE_FLAGS"); ok {
		cfg.FeatureFlags = parseFeatureFlags(value)
	}

	return cfg, cfg.Validate()
}

func parseFeatureFlags(value string) FeatureFlags {
	known := map[string]struct{}{
		FeatureAllowSubqueries:  {},
		FeatureTaggedSubqueries: {},
	}
	seen := map[string]struct{}{}
	flags := FeatureFlags{}

	for _, raw := range strings.Split(value, ",") {
		flag := strings.TrimSpace(raw)
		if flag == "" {
			continue
		}
		if _, ok := known[flag]; !ok {
			continue
		}
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		flags = append(flags, flag)
	}
	return flags
}

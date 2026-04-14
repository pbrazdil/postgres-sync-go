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
			return fmt.Errorf("invalid ELECTRIC_POOLED_DATABASE_URL: %w", err)
		}
	}

	if !c.Insecure && strings.TrimSpace(c.Secret) == "" {
		return errors.New("ELECTRIC_SECRET is required unless ELECTRIC_INSECURE=true")
	}

	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid ELECTRIC_PORT: %d", c.Port)
	}

	if c.DBPoolSize <= 0 {
		return fmt.Errorf("invalid ELECTRIC_DB_POOL_SIZE: %d", c.DBPoolSize)
	}

	if c.MaxConcurrentRequests.Initial <= 0 || c.MaxConcurrentRequests.Existing <= 0 {
		return fmt.Errorf("invalid ELECTRIC_MAX_CONCURRENT_REQUESTS: %+v", c.MaxConcurrentRequests)
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

	if value, ok := lookup("ELECTRIC_POOLED_DATABASE_URL"); ok && strings.TrimSpace(value) != "" {
		cfg.PooledDatabaseURL = strings.TrimSpace(value)
	}

	if cfg.PooledDatabaseURL == "" {
		cfg.PooledDatabaseURL = cfg.DatabaseURL
	}

	if value, ok := lookup("ELECTRIC_SECRET"); ok {
		cfg.Secret = strings.TrimSpace(value)
	}

	if value, ok := lookup("ELECTRIC_INSECURE"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_INSECURE: %w", err)
		}
		cfg.Insecure = parsed
	}

	if value, ok := lookup("ELECTRIC_PORT"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_PORT: %w", err)
		}
		cfg.Port = parsed
	}

	if value, ok := lookup("ELECTRIC_REPLICATION_STREAM_ID"); ok && strings.TrimSpace(value) != "" {
		cfg.ReplicationStreamID = strings.TrimSpace(value)
	}

	if value, ok := lookup("ELECTRIC_DB_POOL_SIZE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_DB_POOL_SIZE: %w", err)
		}
		cfg.DBPoolSize = parsed
	}

	if value, ok := lookup("ELECTRIC_CACHE_MAX_AGE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_CACHE_MAX_AGE: %w", err)
		}
		cfg.Cache.MaxAge = parsed
	}

	if value, ok := lookup("ELECTRIC_CACHE_STALE_AGE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_CACHE_STALE_AGE: %w", err)
		}
		cfg.Cache.StaleAge = parsed
	}

	if value, ok := lookup("ELECTRIC_MAX_CONCURRENT_REQUESTS"); ok && strings.TrimSpace(value) != "" {
		if err := json.Unmarshal([]byte(value), &cfg.MaxConcurrentRequests); err != nil {
			return Config{}, fmt.Errorf("invalid ELECTRIC_MAX_CONCURRENT_REQUESTS: %w", err)
		}
	}

	return cfg, cfg.Validate()
}

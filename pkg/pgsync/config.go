package pgsync

import (
	"os"

	internalconfig "github.com/pbrazdil/postgres-sync-go/internal/config"
)

type Config = internalconfig.Config
type CacheConfig = internalconfig.CacheConfig
type FeatureFlags = internalconfig.FeatureFlags
type MaxConcurrentRequests = internalconfig.MaxConcurrentRequests
type StorageConfig = internalconfig.StorageConfig
type StorageMode = internalconfig.StorageMode
type TelemetryConfig = internalconfig.TelemetryConfig

const (
	FeatureAllowSubqueries  = internalconfig.FeatureAllowSubqueries
	FeatureTaggedSubqueries = internalconfig.FeatureTaggedSubqueries
	StorageModeMemory       = internalconfig.StorageModeMemory
	StorageModeDisk         = internalconfig.StorageModeDisk
)

func DefaultConfig() Config {
	return internalconfig.DefaultConfig()
}

func LoadConfigFromEnv() (Config, error) {
	return internalconfig.Loader{LookupEnv: os.LookupEnv}.Load()
}

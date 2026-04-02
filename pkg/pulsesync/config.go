package pulsesync

import (
	"os"

	internalconfig "github.com/petrbrazdil/pulsesync/internal/config"
)

type Config = internalconfig.Config
type CacheConfig = internalconfig.CacheConfig
type MaxConcurrentRequests = internalconfig.MaxConcurrentRequests
type StorageConfig = internalconfig.StorageConfig
type StorageMode = internalconfig.StorageMode
type TelemetryConfig = internalconfig.TelemetryConfig

const (
	StorageModeMemory = internalconfig.StorageModeMemory
	StorageModeDisk   = internalconfig.StorageModeDisk
)

func DefaultConfig() Config {
	return internalconfig.DefaultConfig()
}

func LoadConfigFromEnv() (Config, error) {
	return internalconfig.Loader{LookupEnv: os.LookupEnv}.Load()
}

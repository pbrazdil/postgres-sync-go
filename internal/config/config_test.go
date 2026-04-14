package config

import "testing"

func TestLoaderAppliesDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	loader := Loader{
		LookupEnv: lookupFromMap(map[string]string{
			"DATABASE_URL":                     "postgresql://postgres:postgres@localhost:5432/pulsesync",
			"ELECTRIC_SECRET":                  "test-secret",
			"ELECTRIC_MAX_CONCURRENT_REQUESTS": `{"initial":500,"existing":30000}`,
		}),
	}

	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Port != DefaultPort {
		t.Fatalf("Port = %d, want %d", cfg.Port, DefaultPort)
	}

	if cfg.PooledDatabaseURL != cfg.DatabaseURL {
		t.Fatalf("PooledDatabaseURL = %q, want %q", cfg.PooledDatabaseURL, cfg.DatabaseURL)
	}

	if cfg.MaxConcurrentRequests.Initial != 500 || cfg.MaxConcurrentRequests.Existing != 30000 {
		t.Fatalf("MaxConcurrentRequests = %+v", cfg.MaxConcurrentRequests)
	}
}

func TestLoaderRequiresSecretUnlessInsecure(t *testing.T) {
	t.Parallel()

	loader := Loader{
		LookupEnv: lookupFromMap(map[string]string{
			"DATABASE_URL": "postgresql://postgres:postgres@localhost:5432/pulsesync",
		}),
	}

	if _, err := loader.Load(); err == nil {
		t.Fatalf("Load() error = nil, want error")
	}

	insecureLoader := Loader{
		LookupEnv: lookupFromMap(map[string]string{
			"DATABASE_URL":      "postgresql://postgres:postgres@localhost:5432/pulsesync",
			"ELECTRIC_INSECURE": "true",
		}),
	}

	if _, err := insecureLoader.Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
}

func lookupFromMap(values map[string]string) LookupEnvFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

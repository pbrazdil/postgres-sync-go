package config

import "testing"

func TestLoaderAppliesDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	loader := Loader{
		LookupEnv: lookupFromMap(map[string]string{
			"DATABASE_URL":                 "postgresql://postgres:postgres@localhost:5432/pulsesync",
			"SYNC_SECRET":                  "test-secret",
			"SYNC_MAX_CONCURRENT_REQUESTS": `{"initial":500,"existing":30000}`,
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
			"DATABASE_URL":  "postgresql://postgres:postgres@localhost:5432/pulsesync",
			"SYNC_INSECURE": "true",
		}),
	}

	if _, err := insecureLoader.Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
}

func TestLoaderParsesKnownFeatureFlags(t *testing.T) {
	t.Parallel()

	loader := Loader{
		LookupEnv: lookupFromMap(map[string]string{
			"DATABASE_URL":       "postgresql://postgres:postgres@localhost:5432/pulsesync",
			"SYNC_SECRET":        "test-secret",
			"SYNC_FEATURE_FLAGS": "allow_subqueries,unknown, tagged_subqueries, allow_subqueries",
		}),
	}

	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.FeatureFlags.Enabled(FeatureAllowSubqueries) {
		t.Fatalf("expected allow_subqueries to be enabled")
	}
	if !cfg.FeatureFlags.Enabled(FeatureTaggedSubqueries) {
		t.Fatalf("expected tagged_subqueries to be enabled")
	}
	if cfg.FeatureFlags.Enabled("unknown") {
		t.Fatalf("unexpected unknown feature flag")
	}
	if len(cfg.FeatureFlags) != 2 {
		t.Fatalf("FeatureFlags = %+v, want two known unique flags", cfg.FeatureFlags)
	}
}

func lookupFromMap(values map[string]string) LookupEnvFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

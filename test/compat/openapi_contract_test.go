package compat_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIShapeSpec struct {
	Paths map[string]struct {
		Get struct {
			Parameters []struct {
				Name string `yaml:"name"`
			} `yaml:"parameters"`
		} `yaml:"get"`
	} `yaml:"paths"`
}

func TestElectricOpenAPIFixtureIncludesCoreShapeParameters(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "electric", "website", "electric-api.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	var spec openAPIShapeSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}

	shapePath, ok := spec.Paths["/v1/shape"]
	if !ok {
		t.Fatalf("missing /v1/shape path in Electric OpenAPI fixture")
	}

	names := map[string]bool{}
	for _, parameter := range shapePath.Get.Parameters {
		names[parameter.Name] = true
	}

	for _, required := range []string{"table", "offset", "live", "live_sse", "experimental_live_sse", "handle", "where", "params", "columns"} {
		if !names[required] {
			t.Fatalf("missing parameter %q in Electric OpenAPI fixture", required)
		}
	}
}

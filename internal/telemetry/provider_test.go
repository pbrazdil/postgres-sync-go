package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
)

func TestProviderServesAdmissionLimitMetrics(t *testing.T) {
	t.Parallel()

	provider := NewProvider("test-version", config.TelemetryConfig{}, config.MaxConcurrentRequests{
		Initial:  7,
		Existing: 11,
	})

	rec := httptest.NewRecorder()
	provider.ServeMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`postgres_sync_go_info{version="test-version"} 1`,
		`postgres_sync_go_admission_control_limit{kind="initial"} 7`,
		`postgres_sync_go_admission_control_limit{kind="existing"} 11`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

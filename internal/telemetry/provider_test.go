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
	provider.RecordShapeRequest("initial")
	provider.RecordShapeRequest("existing")
	provider.RecordShapeOverload("existing")
	provider.AttachRuntimeMetrics(func() RuntimeMetrics {
		return RuntimeMetrics{
			Status:               "active",
			ReplicationConnected: true,
			ReplicationSlot:      "slot-1",
			LastConfirmedBytes:   10,
			LastReceivedBytes:    12,
			ServerWALEndBytes:    15,
			WALRetainedBytes:     5,
			Reconnects:           2,
			ReplicationErrors:    1,
			ChangeBatches:        3,
			ChangeRecords:        4,
			Invalidations: map[string]uint64{
				"schema_change": 1,
			},
		}
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
		`postgres_sync_go_shape_requests_total{kind="initial"} 1`,
		`postgres_sync_go_shape_requests_total{kind="existing"} 1`,
		`postgres_sync_go_shape_overloads_total{kind="existing"} 1`,
		`postgres_sync_go_runtime_status{status="active"} 1`,
		`postgres_sync_go_replication_connected 1`,
		`postgres_sync_go_wal_retained_bytes 5`,
		`postgres_sync_go_replication_reconnects_total 2`,
		`postgres_sync_go_change_records_total 4`,
		`postgres_sync_go_invalidations_total{reason="schema_change"} 1`,
		`postgres_sync_go_replication_slot_info{slot="slot-1"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

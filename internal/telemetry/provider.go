package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
	"github.com/pbrazdil/postgres-sync-go/internal/storage"
)

type RuntimeMetrics struct {
	Status               string
	ReplicationConnected bool
	ReplicationSlot      string
	LastConfirmedLSN     string
	LastConfirmedBytes   uint64
	LastReceivedLSN      string
	LastReceivedBytes    uint64
	ServerWALEnd         string
	ServerWALEndBytes    uint64
	WALRetainedBytes     uint64
	Reconnects           uint64
	ReplicationErrors    uint64
	ChangeBatches        uint64
	ChangeRecords        uint64
	Invalidations        map[string]uint64
	LastReplicationError string
}

type Provider struct {
	version         string
	cfg             config.TelemetryConfig
	admissionLimits config.MaxConcurrentRequests
	runtimeMetrics  func() RuntimeMetrics
	store           storage.Store
	shapeRequests   [2]atomic.Uint64
	shapeOverloads  [2]atomic.Uint64
}

func NewProvider(version string, cfg config.TelemetryConfig, admissionLimits config.MaxConcurrentRequests) *Provider {
	if cfg.MetricsPath == "" {
		cfg.MetricsPath = "/metrics"
	}

	return &Provider{
		version:         version,
		cfg:             cfg,
		admissionLimits: admissionLimits,
	}
}

func (p *Provider) AttachRuntimeMetrics(snapshot func() RuntimeMetrics) {
	p.runtimeMetrics = snapshot
}

func (p *Provider) AttachStore(store storage.Store) {
	p.store = store
}

func (p *Provider) RecordShapeRequest(kind string) {
	p.counterForKind(kind, &p.shapeRequests).Add(1)
}

func (p *Provider) RecordShapeOverload(kind string) {
	p.counterForKind(kind, &p.shapeOverloads).Add(1)
}

func (p *Provider) MetricsPath() string {
	return p.cfg.MetricsPath
}

func (p *Provider) ServeMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_info Static postgres-sync-go build information.\n# TYPE postgres_sync_go_info gauge\npostgres_sync_go_info{version=%q} 1\n",
		p.version,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_admission_control_limit Configured concurrent Shape request limit.\n# TYPE postgres_sync_go_admission_control_limit gauge\npostgres_sync_go_admission_control_limit{kind=\"initial\"} %d\npostgres_sync_go_admission_control_limit{kind=\"existing\"} %d\n",
		p.admissionLimits.Initial,
		p.admissionLimits.Existing,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_shape_requests_total Shape requests admitted by admission bucket.\n# TYPE postgres_sync_go_shape_requests_total counter\npostgres_sync_go_shape_requests_total{kind=\"initial\"} %d\npostgres_sync_go_shape_requests_total{kind=\"existing\"} %d\n",
		p.shapeRequests[0].Load(),
		p.shapeRequests[1].Load(),
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_shape_overloads_total Shape requests rejected by admission bucket.\n# TYPE postgres_sync_go_shape_overloads_total counter\npostgres_sync_go_shape_overloads_total{kind=\"initial\"} %d\npostgres_sync_go_shape_overloads_total{kind=\"existing\"} %d\n",
		p.shapeOverloads[0].Load(),
		p.shapeOverloads[1].Load(),
	)
	p.writeRuntimeMetrics(w)
	p.writeStoreMetrics(w, context.Background())
}

func (p *Provider) Close(context.Context) error {
	return nil
}

func (p *Provider) writeRuntimeMetrics(w http.ResponseWriter) {
	if p.runtimeMetrics == nil {
		return
	}
	metrics := p.runtimeMetrics()
	connected := 0
	if metrics.ReplicationConnected {
		connected = 1
	}
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_runtime_status Current runtime status as a labeled gauge.\n# TYPE postgres_sync_go_runtime_status gauge\npostgres_sync_go_runtime_status{status=%q} 1\n",
		metrics.Status,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_replication_connected Whether the logical replication stream is connected.\n# TYPE postgres_sync_go_replication_connected gauge\npostgres_sync_go_replication_connected %d\n",
		connected,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_replication_lsn_bytes Runtime replication LSN positions converted to bytes.\n# TYPE postgres_sync_go_replication_lsn_bytes gauge\npostgres_sync_go_replication_lsn_bytes{kind=\"confirmed\"} %d\npostgres_sync_go_replication_lsn_bytes{kind=\"received\"} %d\npostgres_sync_go_replication_lsn_bytes{kind=\"server_wal_end\"} %d\n",
		metrics.LastConfirmedBytes,
		metrics.LastReceivedBytes,
		metrics.ServerWALEndBytes,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_wal_retained_bytes Approximate bytes between server WAL end and confirmed replication LSN.\n# TYPE postgres_sync_go_wal_retained_bytes gauge\npostgres_sync_go_wal_retained_bytes %d\n",
		metrics.WALRetainedBytes,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_replication_reconnects_total Replication reconnect attempts after a connected stream failed.\n# TYPE postgres_sync_go_replication_reconnects_total counter\npostgres_sync_go_replication_reconnects_total %d\n",
		metrics.Reconnects,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_replication_errors_total Replication stream errors.\n# TYPE postgres_sync_go_replication_errors_total counter\npostgres_sync_go_replication_errors_total %d\n",
		metrics.ReplicationErrors,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_change_batches_total Applied logical replication transactions.\n# TYPE postgres_sync_go_change_batches_total counter\npostgres_sync_go_change_batches_total %d\n",
		metrics.ChangeBatches,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_change_records_total Changed primary keys processed from logical replication.\n# TYPE postgres_sync_go_change_records_total counter\npostgres_sync_go_change_records_total %d\n",
		metrics.ChangeRecords,
	)
	fmt.Fprint(
		w,
		"# HELP postgres_sync_go_invalidations_total Shape invalidations by reason.\n# TYPE postgres_sync_go_invalidations_total counter\n",
	)
	if len(metrics.Invalidations) == 0 {
		fmt.Fprint(w, "postgres_sync_go_invalidations_total{reason=\"none\"} 0\n")
	} else {
		reasons := sortedInvalidationReasons(metrics.Invalidations)
		for _, reason := range reasons {
			fmt.Fprintf(w, "postgres_sync_go_invalidations_total{reason=%q} %d\n", reason, metrics.Invalidations[reason])
		}
	}
	if metrics.ReplicationSlot != "" {
		fmt.Fprintf(
			w,
			"# HELP postgres_sync_go_replication_slot_info Current replication slot label.\n# TYPE postgres_sync_go_replication_slot_info gauge\npostgres_sync_go_replication_slot_info{slot=%q} 1\n",
			metrics.ReplicationSlot,
		)
	}
	if strings.TrimSpace(metrics.LastReplicationError) != "" {
		fmt.Fprintf(
			w,
			"# HELP postgres_sync_go_last_replication_error_info Last replication error label.\n# TYPE postgres_sync_go_last_replication_error_info gauge\npostgres_sync_go_last_replication_error_info{message=%q} 1\n",
			metrics.LastReplicationError,
		)
	}
}

func (p *Provider) writeStoreMetrics(w http.ResponseWriter, ctx context.Context) {
	if p.store == nil {
		return
	}
	stats, err := p.store.Stats(ctx)
	if err != nil {
		fmt.Fprintf(
			w,
			"# HELP postgres_sync_go_storage_stats_error Storage stats collection error.\n# TYPE postgres_sync_go_storage_stats_error gauge\npostgres_sync_go_storage_stats_error{message=%q} 1\n",
			err.Error(),
		)
		return
	}
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_storage_shapes Shape catalog entries by state.\n# TYPE postgres_sync_go_storage_shapes gauge\npostgres_sync_go_storage_shapes{state=\"active\",kind=%q} %d\npostgres_sync_go_storage_shapes{state=\"deleted\",kind=%q} %d\npostgres_sync_go_storage_shapes{state=\"total\",kind=%q} %d\n",
		stats.Kind,
		stats.ActiveShapeCount,
		stats.Kind,
		stats.DeletedShapeCount,
		stats.Kind,
		stats.ShapeCount,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_storage_chunks Persisted change-log chunks and messages.\n# TYPE postgres_sync_go_storage_chunks gauge\npostgres_sync_go_storage_chunks{kind=\"files\",store=%q} %d\npostgres_sync_go_storage_chunks{kind=\"messages\",store=%q} %d\n",
		stats.Kind,
		stats.ChunkCount,
		stats.Kind,
		stats.ChangeCount,
	)
	fmt.Fprintf(
		w,
		"# HELP postgres_sync_go_storage_bytes Storage bytes by component.\n# TYPE postgres_sync_go_storage_bytes gauge\npostgres_sync_go_storage_bytes{component=\"metadata\",kind=%q} %d\npostgres_sync_go_storage_bytes{component=\"chunks\",kind=%q} %d\npostgres_sync_go_storage_bytes{component=\"total\",kind=%q} %d\n",
		stats.Kind,
		stats.MetadataBytes,
		stats.Kind,
		stats.ChunkBytes,
		stats.Kind,
		stats.TotalBytes,
	)
	if stats.HasCheckpoint {
		fmt.Fprintf(
			w,
			"# HELP postgres_sync_go_runtime_checkpoint_info Persisted runtime checkpoint label.\n# TYPE postgres_sync_go_runtime_checkpoint_info gauge\npostgres_sync_go_runtime_checkpoint_info{slot=%q,lsn=%q,db=%q} 1\n",
			stats.Checkpoint.SlotName,
			stats.Checkpoint.LastConfirmedLSN,
			stats.Checkpoint.DBName,
		)
	}
}

func (p *Provider) counterForKind(kind string, counters *[2]atomic.Uint64) *atomic.Uint64 {
	if kind == "existing" {
		return &counters[1]
	}
	return &counters[0]
}

func sortedInvalidationReasons(values map[string]uint64) []string {
	reasons := make([]string, 0, len(values))
	for reason := range values {
		reasons = append(reasons, reason)
	}
	for i := 1; i < len(reasons); i++ {
		for j := i; j > 0 && reasons[j] < reasons[j-1]; j-- {
			reasons[j], reasons[j-1] = reasons[j-1], reasons[j]
		}
	}
	return reasons
}

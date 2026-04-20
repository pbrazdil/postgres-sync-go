package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
)

type Provider struct {
	version         string
	cfg             config.TelemetryConfig
	admissionLimits config.MaxConcurrentRequests
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
}

func (p *Provider) Close(context.Context) error {
	return nil
}

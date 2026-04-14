package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/petrbrazdil/pulsesync/internal/config"
)

type Provider struct {
	version string
	cfg     config.TelemetryConfig
}

func NewProvider(version string, cfg config.TelemetryConfig) *Provider {
	if cfg.MetricsPath == "" {
		cfg.MetricsPath = "/metrics"
	}

	return &Provider{
		version: version,
		cfg:     cfg,
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
		"# HELP pulsesync_info Static PulseSync build information.\n# TYPE pulsesync_info gauge\npulsesync_info{version=%q} 1\n",
		p.version,
	)
}

func (p *Provider) Close(context.Context) error {
	return nil
}

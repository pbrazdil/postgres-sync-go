package httpapi

import (
	"net/http"

	"github.com/pbrazdil/postgres-sync-go/internal/pg"
	"github.com/pbrazdil/postgres-sync-go/internal/protocol"
	"github.com/pbrazdil/postgres-sync-go/internal/telemetry"
)

type statusProvider interface {
	Status() pg.ServiceStatus
}

type Router struct {
	serverHeader string
	protocol     *protocol.Service
	telemetry    *telemetry.Provider
	runtime      statusProvider
}

func NewRouter(serverHeader string, svc *protocol.Service, telemetryProvider *telemetry.Provider, runtime statusProvider) *Router {
	return &Router{
		serverHeader: serverHeader,
		protocol:     svc,
		telemetry:    telemetryProvider,
		runtime:      runtime,
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("electric-server", r.serverHeader)

	switch req.URL.Path {
	case "/":
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
	case "/v1/health":
		r.putStandardCORS(w, req, false)
		r.serveHealth(w)
		return
	case "/v1/shape":
		r.putStandardCORS(w, req, true)
		if req.Method == http.MethodOptions {
			r.serveShapeOptions(w, req)
			return
		}
		if req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodPost || req.Method == http.MethodDelete {
			r.protocol.HandleShape(w, req)
			return
		}
	case r.telemetry.MetricsPath():
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			r.telemetry.ServeMetrics(w, req)
			return
		}
	}

	http.NotFound(w, req)
}

func (r *Router) serveHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	statusCode := http.StatusAccepted
	statusText := "starting"

	switch r.runtime.Status() {
	case pg.StatusActive, pg.StatusSleeping:
		statusCode = http.StatusOK
		statusText = "active"
	case pg.StatusWaiting:
		statusText = "waiting"
	case pg.StatusStarting, pg.StatusStopped:
		statusText = "starting"
	}

	protocol.WriteJSON(w, statusCode, map[string]string{"status": statusText})
}

func (r *Router) serveShapeOptions(w http.ResponseWriter, req *http.Request) {
	if requested := req.Header.Get("Access-Control-Request-Headers"); requested != "" {
		w.Header().Set("Access-Control-Allow-Headers", requested)
	}

	w.Header().Set("Access-Control-Max-Age", "86400")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) putStandardCORS(w http.ResponseWriter, req *http.Request, shape bool) {
	w.Header().Set("Access-Control-Allow-Origin", allowedOrigin(req))
	w.Header().Set("Access-Control-Expose-Headers", protocol.ExposedHeadersValue())

	if shape {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, HEAD, DELETE, OPTIONS")
		return
	}

	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
}

func allowedOrigin(req *http.Request) string {
	if origin := req.Header.Get("Origin"); origin != "" {
		return origin
	}

	return "*"
}

package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
)

var electricHeaders = []string{
	"electric-cursor",
	"electric-handle",
	"electric-has-data",
	"electric-offset",
	"electric-schema",
	"electric-snapshot",
	"electric-up-to-date",
	"electric-internal-known-error",
	"retry-after",
}

func ElectricHeaders() []string {
	cloned := make([]string, len(electricHeaders))
	copy(cloned, electricHeaders)
	return cloned
}

func ExposedHeadersValue() string {
	return strings.Join(ElectricHeaders(), ",")
}

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func WriteError(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Surrogate-Control", "no-store")
	WriteJSON(w, status, body)
}

func WriteInvalidRequest(w http.ResponseWriter, errors ValidationErrors) {
	WriteInvalidRequestBody(w, errors)
}

func WriteInvalidRequestBody(w http.ResponseWriter, errors any) {
	WriteError(w, http.StatusBadRequest, map[string]any{
		"message": "Invalid request",
		"errors":  errors,
	})
}

func WriteUnauthorized(w http.ResponseWriter) {
	WriteError(w, http.StatusUnauthorized, map[string]any{
		"message": "Unauthorized - Invalid API secret",
	})
}

func WriteOverloaded(w http.ResponseWriter) {
	WriteError(w, http.StatusServiceUnavailable, map[string]any{
		"message": "Too many concurrent shape requests",
	})
}

func WriteOverloadedWithRequest(w http.ResponseWriter, req ShapeRequest, limits config.MaxConcurrentRequests) {
	kind := "existing"
	limit := limits.Existing
	if req.Offset == "-1" || req.Offset == "now" || req.Handle == "" {
		kind = "initial"
		limit = limits.Initial
	}

	w.Header().Set("Retry-After", "10")
	w.Header().Set("electric-internal-known-error", "true")
	WriteError(w, http.StatusServiceUnavailable, map[string]any{
		"code":    "concurrent_request_limit_exceeded",
		"message": fmt.Sprintf("Concurrent %s request limit exceeded (limit: %d), please retry", kind, limit),
	})
}

func WriteShapeShellUnavailable(w http.ResponseWriter) {
	WriteError(w, http.StatusServiceUnavailable, map[string]any{
		"message": "postgres-sync-go protocol shell is active, but shape serving is not implemented yet",
		"code":    "not_implemented",
	})
}

type SuccessHeaderOptions struct {
	HasData   bool
	UpToDate  bool
	NoChanges bool
}

var cursorEpoch = time.Date(2024, time.October, 9, 0, 0, 0, 0, time.UTC)

func WriteSuccessHeaders(w http.ResponseWriter, cfg config.Config, req ShapeRequest, state shapes.State, opts SuccessHeaderOptions) {
	offset := state.CurrentOffset
	hasData := fmt.Sprintf("%t", opts.HasData)
	if req.LiveSSE {
		offset = shapes.NowOffset
		hasData = "true"
	}

	w.Header().Set("electric-handle", state.Handle)
	w.Header().Set("electric-offset", offset)
	w.Header().Set("electric-has-data", hasData)
	if opts.UpToDate {
		w.Header().Set("electric-up-to-date", "")
	} else {
		w.Header().Del("electric-up-to-date")
	}
	if req.Live {
		w.Header().Set("electric-cursor", nextCursor(cfg.LongPollTimeoutMS, req.Cursor))
	} else {
		w.Header().Del("electric-cursor")
	}
	w.Header().Set("etag", fmt.Sprintf("%q", ETag(state.Handle, req.Offset, state.CurrentOffset, opts.NoChanges)))
	w.Header().Set("cache-control", cacheControlValue(cfg, req))

	if !req.Live {
		if schema := state.Schema; len(schema) > 0 {
			WriteSchemaHeader(w, schema)
		}
	} else {
		w.Header().Del("electric-schema")
	}
}

func WriteSchemaHeader(w http.ResponseWriter, schema map[string]shapes.ColumnSchema) {
	encoded, _ := json.Marshal(schema)
	w.Header().Set("electric-schema", string(encoded))
}

func WriteSubsetHeaders(w http.ResponseWriter, state shapes.State, schema map[string]shapes.ColumnSchema) {
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("electric-snapshot", "true")
	w.Header().Set("electric-handle", state.Handle)
	w.Header().Set("electric-offset", state.CurrentOffset)
	w.Header().Del("electric-cursor")
	w.Header().Del("electric-has-data")
	w.Header().Del("electric-up-to-date")
	w.Header().Del("etag")

	if len(schema) > 0 {
		WriteSchemaHeader(w, schema)
	} else {
		w.Header().Del("electric-schema")
	}
}

func WriteMustRefetchHeaders(w http.ResponseWriter, req ShapeRequest, state shapes.State) {
	w.Header().Set("electric-handle", state.Handle)
	w.Header().Set("etag", fmt.Sprintf("%q", ETag(state.Handle, req.Offset, state.CurrentOffset, false)))
	w.Header().Set("cache-control", mustRefetchCacheControlValue(state.Handle))
	w.Header().Del("electric-cursor")
	w.Header().Del("electric-has-data")
	w.Header().Del("electric-offset")
	w.Header().Del("electric-schema")
	w.Header().Del("electric-up-to-date")
}

func nextCursor(longPollTimeoutMS int, prevCursor string) string {
	seconds := longPollTimeoutMS / 1000
	if seconds == 0 {
		return "0"
	}

	diff := int(time.Now().UTC().Sub(cursorEpoch).Seconds())
	next := ((diff + seconds - 1) / seconds) * seconds
	cursor := strconv.Itoa(next)
	if cursor == prevCursor {
		next += int(time.Now().UnixNano()%3600) + 1
	}

	return strconv.Itoa(next)
}

func ETag(handle string, requestOffset string, currentOffset string, noChanges bool) string {
	value := handle + ":" + requestOffset + ":" + currentOffset
	if noChanges {
		value += ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	return value
}

func cacheControlValue(cfg config.Config, req ShapeRequest) string {
	switch {
	case req.Offset == "-1":
		return "public, max-age=604800, s-maxage=3600, stale-while-revalidate=2629746"
	case req.Live && req.LiveSSE:
		return fmt.Sprintf("public, max-age=%d", max(1, cfg.SSETimeoutMS/1000-1))
	case req.Live:
		return "public, max-age=5, stale-while-revalidate=5"
	default:
		return fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d", cfg.Cache.MaxAge, cfg.Cache.StaleAge)
	}
}

func WriteNotModified(w http.ResponseWriter, cfg config.Config, req ShapeRequest, state shapes.State, opts SuccessHeaderOptions) {
	WriteSuccessHeaders(w, cfg, req, state, opts)
	w.WriteHeader(http.StatusNotModified)
}

func mustRefetchCacheControlValue(handle string) string {
	if handle == "" {
		return "public, max-age=1, must-revalidate"
	}

	return "public, max-age=60, must-revalidate"
}

func SSEPayload(messages []any) string {
	var buffer bytes.Buffer
	for _, message := range messages {
		encoded, _ := json.Marshal(message)
		buffer.WriteString("data: ")
		buffer.Write(encoded)
		buffer.WriteString("\n\n")
	}
	return buffer.String()
}

package compat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petrbrazdil/pulsesync/pkg/pulsesync"
)

func TestRouterRootAndHealthLifecycle(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	handler := engine.Handler()

	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK || root.Body.String() != "" {
		t.Fatalf("root response = (%d, %q)", root.Code, root.Body.String())
	}

	beforeStart := httptest.NewRecorder()
	handler.ServeHTTP(beforeStart, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	assertJSONStatus(t, beforeStart, http.StatusAccepted, "starting")

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	afterStart := httptest.NewRecorder()
	handler.ServeHTTP(afterStart, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	assertJSONStatuses(t, afterStart,
		statusExpectation{Code: http.StatusOK, Status: "active"},
		statusExpectation{Code: http.StatusAccepted, Status: "waiting"},
	)
}

func TestShapeOptionsBypassSecret(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/shape", nil)
	req.Header.Set("Access-Control-Request-Headers", "If-None-Match")
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS code = %d, want 204", rec.Code)
	}

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "If-None-Match" {
		t.Fatalf("Access-Control-Allow-Headers = %q", got)
	}
}

func TestShapeAuthFailure(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=issues&offset=-1", nil)
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if body["message"] != "Unauthorized - Invalid API secret" {
		t.Fatalf("message = %v", body["message"])
	}
}

func TestShapeValidationContract(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/shape?offset=0_0&live_sse=true&secret=test-secret",
		nil,
	)
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if body.Message != "Invalid request" {
		t.Fatalf("message = %q", body.Message)
	}

	if _, ok := body.Errors["table"]; !ok {
		t.Fatalf("expected table validation error: %+v", body.Errors)
	}

	if _, ok := body.Errors["handle"]; !ok {
		t.Fatalf("expected handle validation error: %+v", body.Errors)
	}

	if _, ok := body.Errors["live_sse"]; !ok {
		t.Fatalf("expected live_sse validation error: %+v", body.Errors)
	}
}

func TestShapePostInvalidJSONContract(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/shape?table=issues&offset=0_0&handle=test-handle&secret=test-secret",
		strings.NewReader("{"),
	)
	req.Header.Set("Content-Type", "application/json")
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if body["error"] != "Invalid JSON in request body" {
		t.Fatalf("error = %v", body["error"])
	}
}

func TestShapeShellReturnsServiceUnavailableForValidRequest(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/shape?table=issues&offset=0_0&handle=test-handle&secret=test-secret",
		nil,
	)
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestShapeDeleteRequiresSecretAndOptIn(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/shape?handle=test-handle", nil)
	engine.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func newTestEngine(t *testing.T) *pulsesync.Engine {
	t.Helper()

	cfg := pulsesync.DefaultConfig()
	cfg.DatabaseURL = "postgresql://postgres:postgres@localhost:5432/pulsesync"
	cfg.PooledDatabaseURL = cfg.DatabaseURL
	cfg.Secret = "test-secret"

	engine, err := pulsesync.New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return engine
}

func assertJSONStatus(t *testing.T, rec *httptest.ResponseRecorder, code int, status string) {
	t.Helper()

	assertJSONStatuses(t, rec, statusExpectation{Code: code, Status: status})
}

type statusExpectation struct {
	Code   int
	Status string
}

func assertJSONStatuses(t *testing.T, rec *httptest.ResponseRecorder, expectations ...statusExpectation) {
	t.Helper()

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, expectation := range expectations {
		if rec.Code == expectation.Code && body["status"] == expectation.Status {
			return
		}
	}

	t.Fatalf("unexpected health response = (%d, %q)", rec.Code, body["status"])
}

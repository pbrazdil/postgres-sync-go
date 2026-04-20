package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
	"github.com/pbrazdil/postgres-sync-go/internal/storage"
)

func TestServiceInitialSnapshot(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			},
			Rows: []shapes.Row{
				{"id": "1", "value": "hello"},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if rec.Header().Get("electric-offset") != shapes.InitialOffset {
		t.Fatalf("electric-offset = %q", rec.Header().Get("electric-offset"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; ok {
		t.Fatalf("unexpected electric-up-to-date header: %+v", rec.Header()["Electric-Up-To-Date"])
	}
	if _, ok := rec.Header()["Electric-Cursor"]; ok {
		t.Fatalf("unexpected electric-cursor header: %+v", rec.Header()["Electric-Cursor"])
	}
	if _, ok := rec.Header()["Electric-Schema"]; !ok {
		t.Fatalf("expected electric-schema header")
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got := body[0]["key"]; got != `"public"."items"/"1"` {
		t.Fatalf("first key = %v", got)
	}
	rowHeaders, _ := body[0]["headers"].(map[string]any)
	if relation, _ := rowHeaders["relation"].([]any); len(relation) != 2 || relation[0] != "public" || relation[1] != "items" {
		t.Fatalf("relation = %+v", rowHeaders["relation"])
	}

	headers, _ := body[1]["headers"].(map[string]any)
	if headers["control"] != "snapshot-end" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceRejectsSubqueriesWithoutFeatureFlag(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshotFunc: func(context.Context, shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
			t.Fatal("snapshot backend should not be called")
			return shapes.SnapshotResult{}, nil
		},
	})

	where := url.QueryEscape("id IN (SELECT item_id FROM item_flags)")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&where="+where+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	errorsObject, ok := body["errors"].(map[string]any)
	if !ok {
		t.Fatalf("errors = %#v", body["errors"])
	}
	whereErrors, ok := errorsObject["where"].([]any)
	if !ok || len(whereErrors) != 1 || whereErrors[0] != "Subqueries are not supported" {
		t.Fatalf("where errors = %#v", errorsObject["where"])
	}
}

func TestServiceAllowsSubqueriesWithFeatureFlag(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.FeatureFlags = config.FeatureFlags{config.FeatureAllowSubqueries}

	where := url.QueryEscape("id IN (SELECT item_id FROM item_flags)")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&where="+where+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestServiceRejectsSubsetSubqueriesWithFeatureFlag(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshotFunc: func(context.Context, shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
			t.Fatal("snapshot backend should not be called")
			return shapes.SnapshotResult{}, nil
		},
	})
	service.cfg.FeatureFlags = config.FeatureFlags{config.FeatureAllowSubqueries}

	where := url.QueryEscape("id IN (SELECT item_id FROM item_flags)")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&subset__where="+where+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body struct {
		Errors map[string]map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := body.Errors["subset"]["where"]; len(got) != 1 || got[0] != "Subqueries are not allowed in subsets" {
		t.Fatalf("subset where errors = %+v", got)
	}
}

func TestServiceInitialSnapshotChangesOnlyReturnsSnapshotEndOnly(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			},
			Rows: []shapes.Row{
				{"id": "1", "value": "hidden"},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&log=changes_only&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("electric-has-data") != "true" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(body) != 1 {
		t.Fatalf("body length = %d, want 1", len(body))
	}
	headers, _ := body[0]["headers"].(map[string]any)
	if headers["control"] != "snapshot-end" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceSubsetSnapshotUsesCanonicalHandle(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshotFunc: func(_ context.Context, request shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
			schema := map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			}

			if request.Definition.Subset == nil {
				return shapes.SnapshotResult{
					Schema: schema,
					Rows: []shapes.Row{
						{"id": "1", "value": "a"},
						{"id": "2", "value": "b"},
					},
				}, nil
			}

			row := shapes.Row{"id": "1", "value": "a"}
			if request.Definition.Subset.Offset != nil && *request.Definition.Subset.Offset == 1 {
				row = shapes.Row{"id": "2", "value": "b"}
			}

			return shapes.SnapshotResult{
				Schema: schema,
				Rows:   []shapes.Row{row},
				Metadata: &shapes.SnapshotMetadata{
					SnapshotMark: 7,
					DatabaseLSN:  "0/16B6C50",
					XMin:         "10",
					XMax:         "11",
					XIPList:      []string{"10"},
				},
			}, nil
		},
	})

	first := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&log=changes_only&subset__limit=1&subset__offset=0&subset__order_by=id%20ASC&secret=test-secret", nil)
	service.HandleShape(first, req1)

	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&log=changes_only&subset__limit=1&subset__offset=1&subset__order_by=id%20ASC&secret=test-secret", nil)
	service.HandleShape(second, req2)

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = (%d, %d), want 200", first.Code, second.Code)
	}
	if first.Header().Get("electric-handle") == "" || second.Header().Get("electric-handle") == "" {
		t.Fatalf("missing electric-handle headers")
	}
	if first.Header().Get("electric-handle") != second.Header().Get("electric-handle") {
		t.Fatalf("subset requests rotated handle: %q != %q", first.Header().Get("electric-handle"), second.Header().Get("electric-handle"))
	}
	if first.Header().Get("electric-snapshot") != "true" {
		t.Fatalf("electric-snapshot = %q", first.Header().Get("electric-snapshot"))
	}
	if first.Header().Get("cache-control") != "no-cache" {
		t.Fatalf("cache-control = %q", first.Header().Get("cache-control"))
	}
	if _, ok := first.Header()["Electric-Has-Data"]; ok {
		t.Fatalf("unexpected electric-has-data header: %+v", first.Header()["Electric-Has-Data"])
	}
	if _, ok := first.Header()["Etag"]; ok {
		t.Fatalf("unexpected etag header: %+v", first.Header()["Etag"])
	}

	var body struct {
		Metadata map[string]any   `json:"metadata"`
		Data     []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if body.Metadata["snapshot_mark"] != float64(7) {
		t.Fatalf("snapshot_mark = %v", body.Metadata["snapshot_mark"])
	}
	if len(body.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(body.Data))
	}
	headers, _ := body.Data[0]["headers"].(map[string]any)
	if headers["snapshot_mark"] != float64(7) {
		t.Fatalf("snapshot row mark = %v", headers["snapshot_mark"])
	}
}

func TestServiceSubsetSnapshotPostBodyUsesCanonicalHandle(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshotFunc: func(_ context.Context, request shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
			schema := map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			}

			if request.Definition.Subset == nil {
				return shapes.SnapshotResult{
					Schema: schema,
					Rows: []shapes.Row{
						{"id": "1", "value": "a"},
						{"id": "2", "value": "b"},
					},
				}, nil
			}

			return shapes.SnapshotResult{
				Schema: schema,
				Rows: []shapes.Row{
					{"id": "2", "value": "b"},
				},
				Metadata: &shapes.SnapshotMetadata{
					SnapshotMark: 9,
					DatabaseLSN:  "0/16B6C50",
					XMin:         "10",
					XMax:         "11",
					XIPList:      []string{"10"},
				},
			}, nil
		},
	})

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&log=changes_only&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	body := strings.NewReader(`{"subset":{"where":"value = $1","params":{"1":"b"}}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/shape?table=items&offset=0_0&handle="+url.QueryEscape(handle)+"&log=changes_only&secret=test-secret", body)
	req.Header.Set("Content-Type", "application/json")
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("electric-handle") != handle {
		t.Fatalf("electric-handle = %q, want %q", rec.Header().Get("electric-handle"), handle)
	}
	if rec.Header().Get("electric-snapshot") != "true" {
		t.Fatalf("electric-snapshot = %q", rec.Header().Get("electric-snapshot"))
	}

	var payload struct {
		Metadata map[string]any   `json:"metadata"`
		Data     []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload.Metadata["snapshot_mark"] != float64(9) {
		t.Fatalf("snapshot_mark = %v", payload.Metadata["snapshot_mark"])
	}
	if len(payload.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(payload.Data))
	}
	if payload.Data[0]["key"] != `"public"."items"/"2"` {
		t.Fatalf("key = %v", payload.Data[0]["key"])
	}
}

func TestServiceOffsetNow(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=now&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if rec.Header().Get("electric-has-data") != "false" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; !ok {
		t.Fatalf("expected electric-up-to-date header")
	}
	if _, ok := rec.Header()["Electric-Cursor"]; ok {
		t.Fatalf("unexpected electric-cursor header: %+v", rec.Header()["Electric-Cursor"])
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	headers, _ := body[0]["headers"].(map[string]any)
	if headers["control"] != "up-to-date" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceContinuationUpToDate(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
			Rows: []shapes.Row{
				{"id": "1"},
			},
		},
	})

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))

	handle := initial.Header().Get("electric-handle")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("electric-has-data") != "false" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; !ok {
		t.Fatalf("expected electric-up-to-date header")
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	headers, _ := body[0]["headers"].(map[string]any)
	if headers["control"] != "up-to-date" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceMustRefetchOnHandleMismatch(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))

	oldHandle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&where=id%20%3D%20%241&params[1]=2&offset=0_0&handle="+oldHandle+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}

	if rec.Header().Get("electric-handle") == oldHandle {
		t.Fatalf("electric-handle reused old handle %q", oldHandle)
	}
	if rec.Header().Get("cache-control") != "public, max-age=60, must-revalidate" {
		t.Fatalf("cache-control = %q", rec.Header().Get("cache-control"))
	}
	if _, ok := rec.Header()["Electric-Schema"]; ok {
		t.Fatalf("unexpected electric-schema header: %+v", rec.Header()["Electric-Schema"])
	}
	if _, ok := rec.Header()["Electric-Has-Data"]; ok {
		t.Fatalf("unexpected electric-has-data header: %+v", rec.Header()["Electric-Has-Data"])
	}
	if _, ok := rec.Header()["Electric-Offset"]; ok {
		t.Fatalf("unexpected electric-offset header: %+v", rec.Header()["Electric-Offset"])
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	headers, _ := body[0]["headers"].(map[string]any)
	if headers["control"] != "must-refetch" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceSubsetValidationRequiresOrderByWhenLimited(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle=test-handle&log=changes_only&subset__limit=1&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body struct {
		Message string         `json:"message"`
		Errors  map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body.Message != "Invalid request" {
		t.Fatalf("message = %q", body.Message)
	}
	subset, _ := body.Errors["subset"].(map[string]any)
	if subset == nil {
		t.Fatalf("subset errors = %+v", body.Errors)
	}
	if _, ok := subset["order_by"]; !ok {
		t.Fatalf("subset errors = %+v", subset)
	}
}

func TestServiceSubsetValidationRejectsMalformedSubsetParams(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle=test-handle&log=changes_only&subset__limit=abc&subset__params={&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body struct {
		Errors map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	subset, _ := body.Errors["subset"].(map[string]any)
	if subset == nil {
		t.Fatalf("subset errors = %+v", body.Errors)
	}
	if _, ok := subset["limit"]; !ok {
		t.Fatalf("subset errors = %+v", subset)
	}
	if _, ok := subset["params"]; !ok {
		t.Fatalf("subset errors = %+v", subset)
	}
}

func TestServiceTableNotFoundMapsToValidationError(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshotErr: shapes.RelationNotFoundError{
			Relation: shapes.Relation{Schema: "public", Table: "missing"},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=missing&offset=-1&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(body.Errors["table"]) == 0 {
		t.Fatalf("table errors = %+v", body.Errors)
	}
}

func TestServiceReturns304ForMatchingETag(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil)
	service.HandleShape(first, req)

	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil)
	req2.Header.Set("If-None-Match", first.Header().Get("etag"))
	service.HandleShape(second, req2)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
}

func TestServiceDeleteRequiresExplicitOptIn(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{})
	service.cfg.AllowShapeDeletion = false

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/shape?handle=test-handle&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServiceDeleteByHandle(t *testing.T) {
	t.Parallel()

	service, manager := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.AllowShapeDeletion = true

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/shape?handle="+handle+"&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	if _, ok := manager.LookupByHandle(handle); ok {
		t.Fatalf("shape %q still present after delete", handle)
	}
}

func TestServiceLiveLongPollReceivesAppendedChange(t *testing.T) {
	t.Parallel()

	service, manager := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 200

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&secret=test-secret", nil)
		service.HandleShape(rec, req)
	}()

	time.Sleep(20 * time.Millisecond)
	_, err := manager.Append(handle, []shapes.Message{
		{
			Headers: map[string]any{"operation": "insert"},
			Key:     "1",
			Value:   shapes.Row{"id": "1", "value": "after-live"},
		},
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("live request did not complete")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(body) != 2 {
		t.Fatalf("body length = %d, want 2", len(body))
	}
	if rec.Header().Get("electric-has-data") != "true" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; !ok {
		t.Fatalf("expected electric-up-to-date header")
	}
	if _, ok := rec.Header()["Electric-Schema"]; ok {
		t.Fatalf("unexpected electric-schema header: %+v", rec.Header()["Electric-Schema"])
	}
	if rec.Header().Get("electric-cursor") == "" {
		t.Fatalf("expected electric-cursor header")
	}

	if _, ok := body[0]["offset"]; ok {
		t.Fatalf("unexpected offset in body: %+v", body[0]["offset"])
	}
	if rec.Header().Get("electric-offset") != "0_1" {
		t.Fatalf("electric-offset = %q", rec.Header().Get("electric-offset"))
	}
	headers, _ := body[1]["headers"].(map[string]any)
	if headers["control"] != "up-to-date" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceLiveLongPollReceivesRefreshedUpdate(t *testing.T) {
	t.Parallel()

	service, manager := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id":    {Type: "uuid", PKIndex: intPtr(0)},
				"value": {Type: "text"},
			},
			Rows: []shapes.Row{
				{"id": "1", "value": "before"},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 200

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&log=changes_only&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&log=changes_only&live=true&secret=test-secret", nil)
		service.HandleShape(rec, req)
	}()

	time.Sleep(20 * time.Millisecond)
	_, messages, err := manager.Refresh(handle, shapes.SnapshotResult{
		Schema: map[string]shapes.ColumnSchema{
			"id":    {Type: "uuid", PKIndex: intPtr(0)},
			"value": {Type: "text"},
		},
		Rows: []shapes.Row{
			{"id": "1", "value": "after"},
		},
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Headers["operation"] != "update" {
		t.Fatalf("messages = %+v", messages)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("live request did not complete")
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(body) != 2 {
		t.Fatalf("body length = %d, want 2", len(body))
	}
	if _, ok := body[0]["offset"]; ok {
		t.Fatalf("unexpected offset in body: %+v", body[0]["offset"])
	}
	if rec.Header().Get("electric-offset") != "0_1" {
		t.Fatalf("electric-offset = %q", rec.Header().Get("electric-offset"))
	}
	if body[0]["value"].(map[string]any)["value"] != "after" {
		t.Fatalf("value = %+v", body[0]["value"])
	}
	headers, _ := body[1]["headers"].(map[string]any)
	if headers["control"] != "up-to-date" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceLiveLongPollTimeoutReturnsUpToDateHeaders(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 50

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("electric-has-data") != "false" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; !ok {
		t.Fatalf("expected electric-up-to-date header")
	}
	if _, ok := rec.Header()["Electric-Schema"]; ok {
		t.Fatalf("unexpected electric-schema header: %+v", rec.Header()["Electric-Schema"])
	}
	if rec.Header().Get("electric-cursor") == "" {
		t.Fatalf("expected electric-cursor header")
	}
	if etag := rec.Header().Get("etag"); !strings.HasPrefix(etag, `"`+handle+`:0_0:0_0:`) {
		t.Fatalf("etag = %q", etag)
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(body) != 1 {
		t.Fatalf("body length = %d, want 1", len(body))
	}
	headers, _ := body[0]["headers"].(map[string]any)
	if headers["control"] != "up-to-date" {
		t.Fatalf("control = %v", headers["control"])
	}
}

func TestServiceLiveSSEFormatsEvents(t *testing.T) {
	t.Parallel()

	service, manager := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 200

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = manager.Append(handle, []shapes.Message{
			{
				Headers: map[string]any{"operation": "insert"},
				Key:     "1",
				Value:   shapes.Row{"id": "1"},
			},
		})
	}()

	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&live_sse=true&secret=test-secret", nil).WithContext(ctx)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("cache-control"); got != "public, max-age=59" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := rec.Header().Get("electric-offset"); got != "0_inf" {
		t.Fatalf("electric-offset = %q", got)
	}
	if rec.Header().Get("electric-has-data") != "true" {
		t.Fatalf("electric-has-data = %q", rec.Header().Get("electric-has-data"))
	}
	if _, ok := rec.Header()["Electric-Up-To-Date"]; !ok {
		t.Fatalf("expected electric-up-to-date header")
	}
	if _, ok := rec.Header()["Electric-Schema"]; ok {
		t.Fatalf("unexpected electric-schema header: %+v", rec.Header()["Electric-Schema"])
	}
	if rec.Header().Get("electric-cursor") == "" {
		t.Fatalf("expected electric-cursor header")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data:") || !strings.Contains(body, `"control":"up-to-date"`) {
		t.Fatalf("unexpected sse body: %q", body)
	}
}

func TestServiceLiveSSEEmitsKeepaliveComments(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 1000
	service.cfg.SSETimeoutMS = 10

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&live_sse=true&secret=test-secret", nil).WithContext(ctx)
	service.HandleShape(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `: keep-alive`) {
		t.Fatalf("expected keepalive comment in body: %q", body)
	}
}

func TestServiceLiveSSEKeepaliveIntervalDefaultsToElectricCadence(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(stubBackend{})
	if got := service.sseKeepaliveInterval(); got != 21*time.Second {
		t.Fatalf("sseKeepaliveInterval() = %s, want 21s", got)
	}
}

func TestServiceOverloadRejectsExistingRequests(t *testing.T) {
	t.Parallel()

	service, manager := newTestService(stubBackend{
		snapshot: shapes.SnapshotResult{
			Schema: map[string]shapes.ColumnSchema{
				"id": {Type: "uuid", PKIndex: intPtr(0)},
			},
		},
	})
	service.cfg.LongPollTimeoutMS = 100
	service.cfg.MaxConcurrentRequests.Initial = 10
	service.cfg.MaxConcurrentRequests.Existing = 1
	service.admission = newAdmissionController(10, 1)

	initial := httptest.NewRecorder()
	service.HandleShape(initial, httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=-1&secret=test-secret", nil))
	handle := initial.Header().Get("electric-handle")

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&secret=test-secret", nil).WithContext(ctx)
		service.HandleShape(rec, req)
	}()

	time.Sleep(10 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/shape?table=items&offset=0_0&handle="+handle+"&live=true&secret=test-secret", nil)
	service.HandleShape(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "10" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("electric-internal-known-error") != "true" {
		t.Fatalf("electric-internal-known-error = %q", rec.Header().Get("electric-internal-known-error"))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["code"] != "concurrent_request_limit_exceeded" {
		t.Fatalf("code = %v", body["code"])
	}
	if body["message"] != "Concurrent existing request limit exceeded (limit: 1), please retry" {
		t.Fatalf("message = %v", body["message"])
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for blocked request to finish")
	}

	_, _ = manager.Append(handle, []shapes.Message{
		{
			Headers: map[string]any{"operation": "insert"},
			Key:     "1",
			Value:   shapes.Row{"id": "1"},
		},
	})
}

type stubBackend struct {
	snapshot     shapes.SnapshotResult
	snapshotErr  error
	snapshotFunc func(context.Context, shapes.SnapshotRequest) (shapes.SnapshotResult, error)
}

func (b stubBackend) Snapshot(ctx context.Context, request shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
	if b.snapshotFunc != nil {
		return b.snapshotFunc(ctx, request)
	}
	if b.snapshotErr != nil {
		return shapes.SnapshotResult{}, b.snapshotErr
	}
	return b.snapshot, nil
}

func newTestService(backend shapes.Backend) (*Service, *shapes.Manager) {
	cfg := config.DefaultConfig()
	cfg.DatabaseURL = "postgresql://postgres:postgres@localhost:5432/postgres_sync_go"
	cfg.PooledDatabaseURL = cfg.DatabaseURL
	cfg.Secret = "test-secret"
	cfg.AllowShapeDeletion = true

	manager, err := shapes.NewManager(storage.NewMemoryStore())
	if err != nil {
		panic(err)
	}
	return NewService(cfg, manager, backend), manager
}

func intPtr(value int) *int {
	return &value
}

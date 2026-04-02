package protocol

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

func TestParseShapeRequestMergesPostSubsetBody(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		"POST",
		"/v1/shape?table=issues&offset=0_0&handle=test-handle",
		strings.NewReader(`{"where":"priority = $1","params":{"1":"high"},"order_by":"created_at","limit":10}`),
	)
	req.Header.Set("Content-Type", "application/json")

	parsed, parseErr := ParseShapeRequest(req)
	if parseErr != nil {
		t.Fatalf("ParseShapeRequest() error = %+v", parseErr)
	}

	if parsed.Subset == nil {
		t.Fatalf("Subset = nil, want non-nil")
	}

	if parsed.Subset.Where != "priority = $1" {
		t.Fatalf("Subset.Where = %q", parsed.Subset.Where)
	}

	if parsed.Subset.Params["1"] != "high" {
		t.Fatalf("Subset.Params = %+v", parsed.Subset.Params)
	}

	if parsed.Subset.OrderBy != "created_at" {
		t.Fatalf("Subset.OrderBy = %q", parsed.Subset.OrderBy)
	}

	if parsed.Subset.Limit == nil || *parsed.Subset.Limit != 10 {
		t.Fatalf("Subset.Limit = %v", parsed.Subset.Limit)
	}

	if parsed.Where != "" {
		t.Fatalf("Where = %q, want empty main shape where", parsed.Where)
	}
}

func TestValidateShapeRequest(t *testing.T) {
	t.Parallel()

	errors := ValidateShapeRequest(ShapeRequest{
		TableRaw: "issues",
		Offset:   "0_0",
		LiveSSE:  true,
	})

	if _, ok := errors["handle"]; !ok {
		t.Fatalf("missing handle validation error: %+v", errors)
	}

	if _, ok := errors["live_sse"]; !ok {
		t.Fatalf("missing live_sse validation error: %+v", errors)
	}
}

func TestValidateSubsetRequest(t *testing.T) {
	t.Parallel()

	zero := 0
	negativeOne := -1
	errors := ValidateSubsetRequest(ShapeRequest{
		Subset: &shapes.Subset{
			Limit:  &zero,
			Offset: &negativeOne,
		},
	})

	if _, ok := errors["limit"]; !ok {
		t.Fatalf("missing limit validation error: %+v", errors)
	}
	if _, ok := errors["offset"]; !ok {
		t.Fatalf("missing offset validation error: %+v", errors)
	}
	if _, ok := errors["order_by"]; !ok {
		t.Fatalf("missing order_by validation error: %+v", errors)
	}
}

func TestValidateSubsetRequestRejectsMalformedSubsetParams(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		"GET",
		"/v1/shape?table=items&offset=0_0&handle=test-handle&subset__limit=abc&subset__params={",
		nil,
	)

	parsed, parseErr := ParseShapeRequest(req)
	if parseErr != nil {
		t.Fatalf("ParseShapeRequest() error = %+v", parseErr)
	}

	errors := ValidateSubsetRequest(parsed)
	if _, ok := errors["limit"]; !ok {
		t.Fatalf("missing limit validation error: %+v", errors)
	}
	if _, ok := errors["params"]; !ok {
		t.Fatalf("missing params validation error: %+v", errors)
	}
}

func TestParseShapeRequestRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/v1/shape?table=issues&offset=-1", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")

	_, parseErr := ParseShapeRequest(req)
	if parseErr == nil {
		t.Fatalf("ParseShapeRequest() error = nil, want error")
	}

	if parseErr.Status != 400 {
		t.Fatalf("ParseShapeRequest() status = %d, want 400", parseErr.Status)
	}
}

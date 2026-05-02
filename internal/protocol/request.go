package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/petrbrazdil/pulsesync/internal/shapes"
	"github.com/petrbrazdil/pulsesync/internal/sqlinspect"
)

const maxBodyBytes = 1 << 20

var (
	offsetPattern     = regexp.MustCompile(`^-1$|^now$|^\d+_(?:\d+|inf)$`)
	deepObjectPattern = regexp.MustCompile(`^([a-z_]+)\[(\d+)\]$`)
)

type ShapeRequest struct {
	Method              string
	TableRaw            string
	Relation            shapes.Relation
	TableParseError     string
	Offset              string
	Handle              string
	Cursor              string
	Live                bool
	LiveSSE             bool
	ExperimentalLiveSSE bool
	Where               string
	Params              map[string]string
	ColumnsRaw          string
	Columns             []string
	ColumnsParseError   string
	Replica             string
	Log                 string
	Subset              *shapes.Subset
	SubsetErrors        ValidationErrors
	Secret              string
	LegacySecret        string
}

type ParseError struct {
	Status int
	Body   map[string]any
}

type ValidationErrors map[string][]string

func (e ValidationErrors) Add(field string, message string) {
	e[field] = append(e[field], message)
}

func (e ValidationErrors) Empty() bool {
	return len(e) == 0
}

func ParseShapeRequest(r *http.Request) (ShapeRequest, *ParseError) {
	body, parseErr := parseBody(r)
	if parseErr != nil {
		return ShapeRequest{}, parseErr
	}

	query := r.URL.Query()
	req := ShapeRequest{
		Method:              r.Method,
		TableRaw:            queryStringValue(query, "table"),
		Offset:              queryStringValue(query, "offset"),
		Handle:              queryStringValue(query, "handle"),
		Cursor:              queryStringValue(query, "cursor"),
		Live:                queryBoolValue(query, "live"),
		ExperimentalLiveSSE: queryBoolValue(query, "experimental_live_sse"),
		Where:               queryStringValue(query, "where"),
		Params:              parseQueryStringMap(query, "params"),
		ColumnsRaw:          queryStringValue(query, "columns"),
		Replica:             queryStringValue(query, "replica"),
		Log:                 queryStringValue(query, "log"),
		Secret:              queryStringValue(query, "secret"),
		LegacySecret:        queryStringValue(query, "api_secret"),
	}

	req.LiveSSE = boolValue(body, query, "live_sse") || req.ExperimentalLiveSSE

	if req.TableRaw != "" {
		relation, err := ParseRelation(req.TableRaw)
		if err != nil {
			req.TableParseError = err.Error()
		} else {
			req.Relation = relation
		}
	}

	if req.ColumnsRaw != "" {
		columns, err := ParseColumns(req.ColumnsRaw)
		if err != nil {
			req.ColumnsParseError = err.Error()
		} else {
			req.Columns = columns
		}
	}

	req.Subset, req.SubsetErrors = parseSubset(body, query)

	return req, nil
}

func ValidateShapeRequest(req ShapeRequest) ValidationErrors {
	errors := ValidationErrors{}

	if strings.TrimSpace(req.TableRaw) == "" {
		errors.Add("table", "can't be blank")
	} else if req.TableParseError != "" {
		errors.Add("table", req.TableParseError)
	}

	if strings.TrimSpace(req.Offset) == "" {
		errors.Add("offset", "can't be blank")
	} else if !offsetPattern.MatchString(req.Offset) {
		errors.Add("offset", "has invalid format")
	}

	if req.ColumnsRaw != "" && req.ColumnsParseError != "" {
		errors.Add("columns", req.ColumnsParseError)
	}

	if req.Offset != "" && req.Offset != "-1" && req.Offset != "now" && strings.TrimSpace(req.Handle) == "" {
		errors.Add("handle", "can't be blank when offset != -1")
	}

	if req.Live && req.Offset == "-1" {
		errors.Add("live", "can't be true when offset == -1")
	}

	if req.LiveSSE && !req.Live {
		errors.Add("live_sse", "can't be true unless live is also true")
	}

	return errors
}

func ValidateSubsetRequest(req ShapeRequest) ValidationErrors {
	errors := ValidationErrors{}
	for field, messages := range req.SubsetErrors {
		errors[field] = append(errors[field], messages...)
	}
	if req.Subset == nil {
		return errors
	}

	if req.Subset.Limit != nil && *req.Subset.Limit <= 0 {
		errors.Add("limit", "must be greater than 0")
	}

	if req.Subset.Offset != nil && *req.Subset.Offset < 0 {
		errors.Add("offset", "must be greater than or equal to 0")
	}

	if (req.Subset.Limit != nil || req.Subset.Offset != nil) && strings.TrimSpace(req.Subset.OrderBy) == "" {
		errors.Add("order_by", "order_by is required when limit or offset is present")
	}

	if subsetContainsDependencyKeyword(req.Subset) {
		errors.Add("where", "Subqueries are not allowed in subsets")
	}

	return errors
}

func subsetContainsDependencyKeyword(subset *shapes.Subset) bool {
	if subset == nil {
		return false
	}
	return sqlinspect.ContainsDependencyKeyword(subset.Where) ||
		sqlinspect.ContainsDependencyKeyword(subset.WhereExpr) ||
		sqlinspect.ContainsDependencyKeyword(subset.OrderBy) ||
		sqlinspect.ContainsDependencyKeyword(subset.OrderByExpr)
}

func parseBody(r *http.Request) (map[string]any, *ParseError) {
	if r.Method != http.MethodPost {
		return map[string]any{}, nil
	}

	bodyReader := io.LimitReader(r.Body, maxBodyBytes+1)
	raw, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, &ParseError{
			Status: http.StatusBadRequest,
			Body: map[string]any{
				"error": "Failed to read request body",
			},
		}
	}

	if len(raw) > maxBodyBytes {
		return nil, &ParseError{
			Status: http.StatusRequestEntityTooLarge,
			Body: map[string]any{
				"error": "Request body too large",
			},
		}
	}

	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, &ParseError{
			Status: http.StatusBadRequest,
			Body: map[string]any{
				"error":   "Invalid JSON in request body",
				"details": err.Error(),
			},
		}
	}

	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, &ParseError{
			Status: http.StatusBadRequest,
			Body: map[string]any{
				"error": "Request body must be a JSON object",
			},
		}
	}

	return object, nil
}

func stringValue(body map[string]any, query map[string][]string, key string) string {
	if value, ok := body[key]; ok {
		return toString(value)
	}

	if values, ok := query[key]; ok && len(values) > 0 {
		return values[len(values)-1]
	}

	return ""
}

func queryStringValue(query map[string][]string, key string) string {
	if values, ok := query[key]; ok && len(values) > 0 {
		return values[len(values)-1]
	}
	return ""
}

func boolValue(body map[string]any, query map[string][]string, key string) bool {
	if value, ok := body[key]; ok {
		return truthy(value)
	}

	if values, ok := query[key]; ok && len(values) > 0 {
		return truthy(values[len(values)-1])
	}

	return false
}

func queryBoolValue(query map[string][]string, key string) bool {
	if values, ok := query[key]; ok && len(values) > 0 {
		return truthy(values[len(values)-1])
	}
	return false
}

func parseQueryStringMap(query map[string][]string, prefix string) map[string]string {
	return parseStringMap(map[string]any{}, query, prefix)
}

func parseStringMap(body map[string]any, query map[string][]string, prefix string) map[string]string {
	result := map[string]string{}

	if value, ok := body[prefix]; ok {
		mapsCopy(result, convertValueMap(value))
	}

	for key, values := range query {
		matches := deepObjectPattern.FindStringSubmatch(key)
		if len(matches) == 3 && matches[1] == prefix && len(values) > 0 {
			result[matches[2]] = values[len(values)-1]
		}
	}

	if len(result) > 0 {
		return result
	}

	if values, ok := query[prefix]; ok && len(values) > 0 {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(values[len(values)-1]), &parsed); err == nil {
			return parsed
		}
	}

	return nil
}

func parseSubset(body map[string]any, query map[string][]string) (*shapes.Subset, ValidationErrors) {
	subsetBody := map[string]any{}
	if value, ok := body["subset"]; ok {
		subsetBody = convertAnyMap(value)
	} else {
		for _, key := range []string{"where", "params", "limit", "offset", "order_by", "where_expr", "order_by_expr"} {
			if value, ok := body[key]; ok {
				subsetBody[key] = value
			}
		}
	}

	subsetQuery := map[string]string{}
	for key, values := range query {
		if strings.HasPrefix(key, "subset__") && len(values) > 0 {
			subsetQuery[strings.TrimPrefix(key, "subset__")] = values[len(values)-1]
		}
	}

	if len(subsetBody) == 0 && len(subsetQuery) == 0 {
		return nil, nil
	}

	errors := ValidationErrors{}
	subset := &shapes.Subset{
		Where:       valueFromMaps(subsetBody, subsetQuery, "where"),
		OrderBy:     valueFromMaps(subsetBody, subsetQuery, "order_by"),
		WhereExpr:   valueFromMaps(subsetBody, subsetQuery, "where_expr"),
		OrderByExpr: valueFromMaps(subsetBody, subsetQuery, "order_by_expr"),
	}

	if params, ok := subsetBody["params"]; ok {
		parsed, valid := convertValueMapStrict(params)
		if !valid {
			errors.Add("params", "params must be a JSON object")
		} else {
			subset.Params = parsed
		}
	} else if raw, ok := subsetQuery["params"]; ok {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			subset.Params = parsed
		} else {
			errors.Add("params", "params must be valid JSON")
		}
	}

	if limit, present, valid := intPointerFromMaps(subsetBody, subsetQuery, "limit"); present {
		if valid {
			subset.Limit = limit
		} else {
			errors.Add("limit", "is invalid")
		}
	}
	if offset, present, valid := intPointerFromMaps(subsetBody, subsetQuery, "offset"); present {
		if valid {
			subset.Offset = offset
		} else {
			errors.Add("offset", "is invalid")
		}
	}

	return subset, errors
}

func valueFromMaps(body map[string]any, query map[string]string, key string) string {
	if value, ok := body[key]; ok {
		return toString(value)
	}
	return query[key]
}

func intPointerFromMaps(body map[string]any, query map[string]string, key string) (*int, bool, bool) {
	if value, ok := body[key]; ok {
		parsed, valid := parseIntPointer(value)
		return parsed, true, valid
	}

	if value, ok := query[key]; ok {
		parsed, valid := parseIntPointer(value)
		return parsed, true, valid
	}

	return nil, false, true
}

func parseIntPointer(value any) (*int, bool) {
	switch typed := value.(type) {
	case float64:
		parsed := int(typed)
		return &parsed, true
	case int:
		parsed := typed
		return &parsed, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return nil, false
		}
		return &parsed, true
	default:
		return nil, false
	}
}

func convertAnyMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}

	typed, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}

	return typed
}

func convertValueMap(value any) map[string]string {
	result, _ := convertValueMapStrict(value)
	return result
}

func convertValueMapStrict(value any) (map[string]string, bool) {
	source := convertAnyMap(value)
	if len(source) == 0 {
		return nil, value == nil
	}

	result := make(map[string]string, len(source))
	for key, item := range source {
		result[key] = toString(item)
	}
	return result, true
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.TrimSpace(strings.ToLower(typed)) != "false"
	default:
		return value != nil
	}
}

func mapsCopy(dst map[string]string, src map[string]string) {
	for key, value := range src {
		dst[key] = value
	}
}

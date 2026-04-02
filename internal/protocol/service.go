package protocol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/petrbrazdil/pulsesync/internal/config"
	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

type Service struct {
	cfg     config.Config
	shapes  *shapes.Manager
	backend shapes.Backend
}

func NewService(cfg config.Config, manager *shapes.Manager, backend shapes.Backend) *Service {
	return &Service{
		cfg:     cfg,
		shapes:  manager,
		backend: backend,
	}
}

func (s *Service) HandleShape(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		WriteUnauthorized(w)
		return
	}

	request, parseErr := ParseShapeRequest(r)
	if parseErr != nil {
		WriteError(w, parseErr.Status, parseErr.Body)
		return
	}

	definition := shapeDefinitionFromRequest(request)
	queryDefinition := snapshotDefinitionFromRequest(request)

	if r.Method == http.MethodDelete {
		if validation := validateDeleteRequest(request); !validation.Empty() {
			w.Header().Set("Cache-Control", "no-cache")
			WriteJSON(w, http.StatusBadRequest, map[string]any{
				"message": "Invalid request",
				"errors":  validation,
			})
			return
		}

		s.deleteShape(w, request, definition)
		return
	}

	if validation := ValidateShapeRequest(request); !validation.Empty() {
		WriteInvalidRequest(w, validation)
		return
	}
	if subsetValidation := ValidateSubsetRequest(request); !subsetValidation.Empty() {
		WriteInvalidRequestBody(w, map[string]any{
			"subset": subsetValidation,
		})
		return
	}
	if request.Subset != nil {
		s.serveSubsetSnapshot(w, r, request, definition, queryDefinition)
		return
	}

	switch request.Offset {
	case "-1":
		s.serveInitialSnapshot(w, r, request, queryDefinition)
	case "now":
		s.serveNow(w, r, request, queryDefinition)
	default:
		s.serveContinuation(w, r, request, definition)
	}
}

func (s *Service) authorized(r *http.Request) bool {
	if s.cfg.Insecure || s.cfg.Secret == "" {
		return true
	}

	query := r.URL.Query()
	secret := query.Get("secret")
	if secret == "" {
		secret = query.Get("api_secret")
	}

	return secret == s.cfg.Secret
}

func (s *Service) serveInitialSnapshot(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition) {
	snapshot, err := s.backend.Snapshot(r.Context(), shapes.SnapshotRequest{
		Definition: def,
		Mode:       shapes.SnapshotModeData,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	state := s.shapes.UpsertSnapshot(def, snapshot)
	headers := SuccessHeaderOptions{
		HasData: len(state.Snapshot) > 0,
	}
	WriteSuccessHeaders(w, s.cfg, req, state, headers)

	if matchesIfNoneMatch(r, state, req.Offset, headers.NoChanges) {
		WriteNotModified(w, s.cfg, req, state, headers)
		return
	}

	body := make([]any, 0, len(state.Snapshot)+1)
	for _, message := range state.Snapshot {
		body = append(body, message)
	}
	body = append(body, map[string]any{
		"headers": map[string]any{"control": "snapshot-end"},
	})
	WriteJSON(w, http.StatusOK, body)
}

func (s *Service) serveNow(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition) {
	snapshot, err := s.backend.Snapshot(r.Context(), shapes.SnapshotRequest{
		Definition: def,
		Mode:       shapes.SnapshotModeData,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	state := s.shapes.UpsertSnapshot(def, snapshot)
	headers := SuccessHeaderOptions{
		HasData:   false,
		UpToDate:  true,
		NoChanges: true,
	}
	WriteSuccessHeaders(w, s.cfg, req, state, headers)

	if matchesIfNoneMatch(r, state, req.Offset, headers.NoChanges) {
		WriteNotModified(w, s.cfg, req, state, headers)
		return
	}

	WriteJSON(w, http.StatusOK, []any{
		map[string]any{"headers": map[string]any{"control": "up-to-date"}},
	})
}

func (s *Service) serveContinuation(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition) {
	state, messages, err := s.shapes.Read(req.Handle, req.Offset)
	switch {
	case errors.Is(err, shapes.ErrShapeNotFound), errors.Is(err, shapes.ErrShapeDeleted):
		s.mustRefetch(w, r.Context(), req, def)
		return
	case errors.Is(err, shapes.ErrOffsetOutOfRange):
		WriteInvalidRequest(w, ValidationErrors{
			"offset": {"out of bounds for this shape"},
		})
		return
	case err != nil:
		WriteError(w, http.StatusServiceUnavailable, map[string]any{
			"message": err.Error(),
		})
		return
	}

	_, hash := s.shapes.Canonicalize(def)
	if state.Hash != hash {
		s.mustRefetch(w, r.Context(), req, def)
		return
	}

	if len(messages) == 0 && req.Live {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.cfg.LongPollTimeoutMS)*time.Millisecond)
		defer cancel()

		state, messages, err = s.shapes.WaitForChange(ctx, req.Handle, req.Offset)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			state, messages, err = s.shapes.Read(req.Handle, req.Offset)
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, map[string]any{
					"message": err.Error(),
				})
				return
			}
		case errors.Is(err, shapes.ErrShapeNotFound), errors.Is(err, shapes.ErrShapeDeleted):
			s.mustRefetch(w, r.Context(), req, def)
			return
		case errors.Is(err, shapes.ErrOffsetOutOfRange):
			WriteInvalidRequest(w, ValidationErrors{
				"offset": {"out of bounds for this shape"},
			})
			return
		case err != nil && !errors.Is(err, context.Canceled):
			WriteError(w, http.StatusServiceUnavailable, map[string]any{
				"message": err.Error(),
			})
			return
		}
	}

	headers := SuccessHeaderOptions{
		HasData:   len(messages) > 0,
		UpToDate:  len(messages) == 0 || req.Live,
		NoChanges: len(messages) == 0,
	}
	WriteSuccessHeaders(w, s.cfg, req, state, headers)
	if matchesIfNoneMatch(r, state, req.Offset, headers.NoChanges) {
		WriteNotModified(w, s.cfg, req, state, headers)
		return
	}

	body := make([]any, 0, len(messages)+1)
	for _, message := range messages {
		body = append(body, message)
	}
	if len(messages) == 0 || req.Live {
		body = append(body, map[string]any{"headers": map[string]any{"control": "up-to-date"}})
	}

	if req.LiveSSE {
		s.writeSSE(w, req, body)
		return
	}

	WriteJSON(w, http.StatusOK, body)
}

func (s *Service) mustRefetch(w http.ResponseWriter, ctx context.Context, req ShapeRequest, def shapes.Definition) {
	snapshot, err := s.backend.Snapshot(ctx, shapes.SnapshotRequest{
		Definition: def,
		Mode:       shapes.SnapshotModeValidateOnly,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	state := s.shapes.UpsertSnapshot(def, snapshot)
	WriteMustRefetchHeaders(w, req, state)
	WriteJSON(w, http.StatusConflict, []any{
		map[string]any{"headers": map[string]any{"control": "must-refetch"}},
	})
}

func (s *Service) writeSnapshotError(w http.ResponseWriter, err error) {
	var relationNotFound shapes.RelationNotFoundError
	if errors.As(err, &relationNotFound) {
		WriteInvalidRequest(w, ValidationErrors{
			"table": {relationNotFound.Error()},
		})
		return
	}

	WriteError(w, http.StatusServiceUnavailable, map[string]any{
		"message": err.Error(),
	})
}

func shapeDefinitionFromRequest(request ShapeRequest) shapes.Definition {
	return shapes.Definition{
		Relation: request.Relation,
		Where:    request.Where,
		Params:   request.Params,
		Columns:  request.Columns,
		Replica:  request.Replica,
		Log:      request.Log,
	}
}

func snapshotDefinitionFromRequest(request ShapeRequest) shapes.Definition {
	return shapes.Definition{
		Relation: request.Relation,
		Where:    request.Where,
		Params:   request.Params,
		Columns:  request.Columns,
		Replica:  request.Replica,
		Log:      request.Log,
		Subset:   request.Subset,
	}
}

func matchesIfNoneMatch(r *http.Request, state shapes.State, requestOffset string, noChanges bool) bool {
	return r.Header.Get("If-None-Match") == `"`+ETag(state.Handle, requestOffset, state.CurrentOffset, noChanges)+`"`
}

func (s *Service) deleteShape(w http.ResponseWriter, req ShapeRequest, def shapes.Definition) {
	if !s.cfg.AllowShapeDeletion {
		w.Header().Set("Cache-Control", "no-cache")
		WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"message": "DELETE not allowed",
		})
		return
	}

	if req.Handle != "" {
		_ = s.shapes.Delete(req.Handle)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if state, ok := s.shapes.LookupByDefinition(def); ok {
		_ = s.shapes.Delete(state.Handle)
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) writeSSE(w http.ResponseWriter, req ShapeRequest, body []any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", cacheControlValue(s.cfg, req))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(SSEPayload(body)))
}

func validateDeleteRequest(request ShapeRequest) ValidationErrors {
	errors := ValidationErrors{}

	if request.Handle == "" && request.TableRaw == "" {
		errors.Add("handle", "can't be blank when shape definition is missing")
		return errors
	}

	if request.TableRaw != "" && request.TableParseError != "" {
		errors.Add("table", request.TableParseError)
	}

	return errors
}

func (s *Service) serveSubsetSnapshot(w http.ResponseWriter, r *http.Request, req ShapeRequest, shapeDef shapes.Definition, queryDef shapes.Definition) {
	state, err := s.resolveSubsetShape(r.Context(), req, shapeDef)
	if err != nil {
		s.writeSubsetResolutionError(w, r.Context(), req, shapeDef, err)
		return
	}

	snapshot, err := s.backend.Snapshot(r.Context(), shapes.SnapshotRequest{
		Definition: queryDef,
		Mode:       shapes.SnapshotModeData,
		Metadata:   true,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	metadata := subsetMetadata(snapshot.Metadata)
	WriteSubsetHeaders(w, state, snapshot.Schema)

	WriteJSON(w, http.StatusOK, map[string]any{
		"metadata": metadata,
		"data":     subsetDataMessages(snapshot, metadata.SnapshotMark),
	})
}

func (s *Service) resolveSubsetShape(ctx context.Context, req ShapeRequest, shapeDef shapes.Definition) (shapes.State, error) {
	if req.Handle != "" {
		state, ok := s.shapes.LookupByHandle(req.Handle)
		if !ok {
			return shapes.State{}, shapes.ErrShapeNotFound
		}
		_, hash := s.shapes.Canonicalize(shapeDef)
		if state.Hash != hash {
			return shapes.State{}, shapes.ErrShapeDeleted
		}
		return state, nil
	}

	if state, ok := s.shapes.LookupByDefinition(shapeDef); ok {
		return state, nil
	}

	snapshot, err := s.backend.Snapshot(ctx, shapes.SnapshotRequest{
		Definition: shapeDef,
		Mode:       shapes.SnapshotModeData,
	})
	if err != nil {
		return shapes.State{}, err
	}

	return s.shapes.UpsertSnapshot(shapeDef, snapshot), nil
}

func (s *Service) writeSubsetResolutionError(w http.ResponseWriter, ctx context.Context, req ShapeRequest, shapeDef shapes.Definition, err error) {
	switch {
	case errors.Is(err, shapes.ErrShapeNotFound), errors.Is(err, shapes.ErrShapeDeleted):
		s.mustRefetch(w, ctx, req, shapeDef)
	default:
		s.writeSnapshotError(w, err)
	}
}

func subsetDataMessages(snapshot shapes.SnapshotResult, snapshotMark int) []any {
	messages := make([]any, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		messages = append(messages, map[string]any{
			"headers": map[string]any{
				"operation":     "insert",
				"snapshot_mark": snapshotMark,
			},
			"key":   subsetRowKey(snapshot.Schema, row),
			"value": row,
		})
	}
	return messages
}

func subsetMetadata(metadata *shapes.SnapshotMetadata) *shapes.SnapshotMetadata {
	if metadata != nil {
		return metadata
	}
	return &shapes.SnapshotMetadata{
		SnapshotMark: 1,
		DatabaseLSN:  "0/0",
		XMin:         "0",
		XMax:         "0",
		XIPList:      nil,
	}
}

func subsetRowKey(schema map[string]shapes.ColumnSchema, row shapes.Row) string {
	type pair struct {
		index int
		value string
	}

	pairs := make([]pair, 0, len(schema))
	for name, column := range schema {
		if column.PKIndex == nil {
			continue
		}
		pairs = append(pairs, pair{
			index: *column.PKIndex,
			value: fmt.Sprint(row[name]),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].index < pairs[j].index
	})
	if len(pairs) == 0 {
		return ""
	}

	values := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		values = append(values, pair.value)
	}
	return strings.Join(values, ",")
}

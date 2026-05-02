package protocol

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pbrazdil/postgres-sync-go/internal/config"
	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
	"github.com/pbrazdil/postgres-sync-go/internal/sqlinspect"
)

type Service struct {
	cfg       config.Config
	shapes    *shapes.Manager
	backend   shapes.Backend
	admission *admissionController
	metrics   MetricsRecorder
}

type MetricsRecorder interface {
	RecordShapeRequest(kind string)
	RecordShapeOverload(kind string)
}

const defaultSSEKeepaliveIntervalMS = 21_000

func NewService(cfg config.Config, manager *shapes.Manager, backend shapes.Backend, recorders ...MetricsRecorder) *Service {
	var recorder MetricsRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &Service{
		cfg:     cfg,
		shapes:  manager,
		backend: backend,
		admission: newAdmissionController(
			cfg.MaxConcurrentRequests.Initial,
			cfg.MaxConcurrentRequests.Existing,
		),
		metrics: recorder,
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
	if featureValidation := s.validateFeatureFlags(request); !featureValidation.Empty() {
		WriteInvalidRequest(w, featureValidation)
		return
	}
	if request.Subset != nil {
		s.serveSubsetSnapshot(w, r, request, definition, queryDefinition)
		return
	}

	admissionKind := s.admissionKind(request, definition)
	release, ok := s.admission.acquire(admissionKind)
	if !ok {
		if s.metrics != nil {
			s.metrics.RecordShapeOverload(string(admissionKind))
		}
		WriteOverloadedWithKind(w, admissionKind, s.cfg.MaxConcurrentRequests)
		return
	}
	defer release()
	if s.metrics != nil {
		s.metrics.RecordShapeRequest(string(admissionKind))
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

func (s *Service) validateFeatureFlags(req ShapeRequest) ValidationErrors {
	errors := ValidationErrors{}
	if s.cfg.FeatureFlags.Enabled(config.FeatureAllowSubqueries) {
		return errors
	}

	if requestContainsDependencyKeyword(req) {
		errors.Add("where", "Subqueries are not supported")
	}
	return errors
}

func requestContainsDependencyKeyword(req ShapeRequest) bool {
	return sqlinspect.ContainsDependencyKeyword(req.Where)
}

func (s *Service) admissionKind(req ShapeRequest, def shapes.Definition) admissionKind {
	if req.Handle != "" {
		if state, ok := s.shapes.LookupByHandle(req.Handle); ok && sameShapeDefinition(s.shapes, state, def) {
			return admissionExisting
		}
	}
	if _, ok := s.shapes.LookupByDefinition(def); ok {
		return admissionExisting
	}
	return admissionInitial
}

func sameShapeDefinition(manager *shapes.Manager, state shapes.State, def shapes.Definition) bool {
	_, hash := manager.Canonicalize(def)
	return state.Hash == hash
}

func (s *Service) serveInitialSnapshot(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition) {
	snapshot, err := s.backend.Snapshot(r.Context(), shapes.SnapshotRequest{
		Definition: def,
		Mode:       shapes.SnapshotModeData,
		Metadata:   true,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	state, err := s.shapes.UpsertSnapshot(def, snapshot)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, map[string]any{
			"message": err.Error(),
		})
		return
	}
	headers := SuccessHeaderOptions{
		HasData: len(snapshot.Rows) > 0,
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
	body = append(body, snapshotEndControl(snapshot.Metadata))
	WriteJSON(w, http.StatusOK, body)
}

func (s *Service) serveNow(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition) {
	if state, ok := s.shapes.LookupByDefinition(def); ok {
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
			upToDateControl(currentGlobalLSN(state)),
		})
		return
	}

	snapshot, err := s.backend.Snapshot(r.Context(), shapes.SnapshotRequest{
		Definition: def,
		Mode:       shapes.SnapshotModeData,
		Metadata:   true,
	})
	if err != nil {
		s.writeSnapshotError(w, err)
		return
	}

	state, err := s.shapes.UpsertSnapshotAtOffset(def, snapshot, shapes.NowOffset)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, map[string]any{
			"message": err.Error(),
		})
		return
	}
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
		upToDateControl(snapshotDatabaseLSN(snapshot.Metadata)),
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

	if req.LiveSSE {
		s.serveLiveSSE(w, r, req, def, state, messages)
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
		UpToDate:  true,
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
	body = append(body, upToDateControl(currentGlobalLSN(state)))

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

	state, err := s.shapes.UpsertSnapshot(def, snapshot)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, map[string]any{
			"message": err.Error(),
		})
		return
	}
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
		if _, err := s.shapes.Delete(req.Handle); err != nil {
			WriteError(w, http.StatusServiceUnavailable, map[string]any{
				"message": err.Error(),
			})
			return
		}
		writeDeleteAccepted(w)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if state, ok := s.shapes.LookupByDefinition(def); ok {
		if _, err := s.shapes.Delete(state.Handle); err != nil {
			WriteError(w, http.StatusServiceUnavailable, map[string]any{
				"message": err.Error(),
			})
			return
		}
	}

	writeDeleteAccepted(w)
	w.WriteHeader(http.StatusAccepted)
}

func writeDeleteAccepted(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("electric-has-data", "true")
}

func (s *Service) writeSSE(w http.ResponseWriter, req ShapeRequest, body []any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", cacheControlValue(s.cfg, req))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(SSEPayload(body)))
}

func (s *Service) serveLiveSSE(w http.ResponseWriter, r *http.Request, req ShapeRequest, def shapes.Definition, state shapes.State, messages []shapes.Message) {
	headers := SuccessHeaderOptions{
		HasData:   true,
		UpToDate:  true,
		NoChanges: len(messages) == 0,
	}
	WriteSuccessHeaders(w, s.cfg, req, state, headers)
	if matchesIfNoneMatch(r, state, req.Offset, headers.NoChanges) {
		WriteNotModified(w, s.cfg, req, state, headers)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, map[string]any{
			"message": "streaming is not supported by this response writer",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", cacheControlValue(s.cfg, req))
	w.WriteHeader(http.StatusOK)

	currentOffset := req.Offset
	if len(messages) > 0 {
		s.writeSSEEvents(w, messagesToBody(messages, currentGlobalLSN(state)))
		currentOffset = state.CurrentOffset
	}
	flusher.Flush()

	keepaliveEvery := s.sseKeepaliveInterval()

	writer := bufio.NewWriter(w)
	for {
		waitCtx, cancel := context.WithTimeout(r.Context(), keepaliveEvery)
		nextState, nextMessages, err := s.shapes.WaitForChange(waitCtx, req.Handle, currentOffset)
		cancel()

		switch {
		case errors.Is(err, context.DeadlineExceeded):
			if r.Context().Err() != nil {
				return
			}
			_, _ = writer.WriteString(": keep-alive\n\n")
			_ = writer.Flush()
			flusher.Flush()
			continue
		case errors.Is(err, shapes.ErrShapeNotFound), errors.Is(err, shapes.ErrShapeDeleted):
			s.writeSSEEvents(w, controlBody("must-refetch", ""))
			flusher.Flush()
			return
		case errors.Is(err, shapes.ErrOffsetOutOfRange):
			s.writeSSEEvents(w, controlBody("must-refetch", ""))
			flusher.Flush()
			return
		case err != nil:
			return
		case len(nextMessages) == 0:
			s.writeSSEEvents(w, controlBody("up-to-date", currentGlobalLSN(nextState)))
			flusher.Flush()
		default:
			s.writeSSEEvents(w, messagesToBody(nextMessages, currentGlobalLSN(nextState)))
			flusher.Flush()
			currentOffset = nextState.CurrentOffset
		}
	}
}

func (s *Service) sseKeepaliveInterval() time.Duration {
	intervalMS := defaultSSEKeepaliveIntervalMS
	if s.cfg.SSETimeoutMS > 0 && s.cfg.SSETimeoutMS < intervalMS {
		intervalMS = s.cfg.SSETimeoutMS
	}
	return time.Duration(intervalMS) * time.Millisecond
}

func (s *Service) writeSSEEvents(w http.ResponseWriter, body []any) {
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
		"data":     subsetDataMessages(queryDef.Relation, snapshot, metadata.SnapshotMark),
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

	state, err := s.shapes.UpsertSnapshot(shapeDef, snapshot)
	if err != nil {
		return shapes.State{}, err
	}
	return state, nil
}

func (s *Service) writeSubsetResolutionError(w http.ResponseWriter, ctx context.Context, req ShapeRequest, shapeDef shapes.Definition, err error) {
	switch {
	case errors.Is(err, shapes.ErrShapeNotFound), errors.Is(err, shapes.ErrShapeDeleted):
		s.mustRefetch(w, ctx, req, shapeDef)
	default:
		s.writeSnapshotError(w, err)
	}
}

func subsetDataMessages(relation shapes.Relation, snapshot shapes.SnapshotResult, snapshotMark int) []any {
	messages := make([]any, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		messages = append(messages, map[string]any{
			"headers": map[string]any{
				"operation":     "insert",
				"relation":      shapes.RelationHeader(relation),
				"snapshot_mark": snapshotMark,
			},
			"key":   shapes.MessageKey(relation, snapshot.Schema, row),
			"value": row,
		})
	}
	return messages
}

func messagesToBody(messages []shapes.Message, globalLastSeenLSN string) []any {
	body := make([]any, 0, len(messages)+1)
	for _, message := range messages {
		body = append(body, message)
	}
	body = append(body, upToDateControl(globalLastSeenLSN))
	return body
}

func controlBody(control string, globalLastSeenLSN string) []any {
	return []any{
		controlMessage(control, globalLastSeenLSN),
	}
}

func subsetMetadata(metadata *shapes.SnapshotMetadata) *shapes.SnapshotMetadata {
	if metadata != nil {
		return metadata
	}
	return &shapes.SnapshotMetadata{
		SnapshotMark: 1,
		DatabaseLSN:  "0",
		XMin:         "0",
		XMax:         "0",
		XIPList:      []string{},
	}
}

func snapshotEndControl(metadata *shapes.SnapshotMetadata) map[string]any {
	headers := map[string]any{
		"control":  "snapshot-end",
		"xmin":     "0",
		"xmax":     "0",
		"xip_list": []string{},
	}
	if metadata != nil {
		headers["xmin"] = metadata.XMin
		headers["xmax"] = metadata.XMax
		if metadata.XIPList != nil {
			headers["xip_list"] = metadata.XIPList
		}
	}
	return map[string]any{"headers": headers}
}

func upToDateControl(globalLastSeenLSN string) map[string]any {
	return controlMessage("up-to-date", globalLastSeenLSN)
}

func controlMessage(control string, globalLastSeenLSN string) map[string]any {
	headers := map[string]any{"control": control}
	if control == "up-to-date" && globalLastSeenLSN != "" {
		headers["global_last_seen_lsn"] = globalLastSeenLSN
	}
	return map[string]any{"headers": headers}
}

func snapshotDatabaseLSN(metadata *shapes.SnapshotMetadata) string {
	if metadata == nil {
		return ""
	}
	return metadata.DatabaseLSN
}

func currentGlobalLSN(state shapes.State) string {
	lsn, ok := shapes.OffsetLSN(state.CurrentOffset)
	if ok {
		return lsn
	}
	return ""
}

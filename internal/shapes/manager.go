package shapes

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/petrbrazdil/pulsesync/internal/sqlinspect"
	"github.com/petrbrazdil/pulsesync/internal/storage"
)

type Relation struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

type Subset struct {
	Where       string            `json:"where,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Limit       *int              `json:"limit,omitempty"`
	Offset      *int              `json:"offset,omitempty"`
	OrderBy     string            `json:"order_by,omitempty"`
	WhereExpr   string            `json:"where_expr,omitempty"`
	OrderByExpr string            `json:"order_by_expr,omitempty"`
}

type Definition struct {
	Relation Relation          `json:"relation"`
	Where    string            `json:"where,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Columns  []string          `json:"columns,omitempty"`
	Replica  string            `json:"replica,omitempty"`
	Log      string            `json:"log,omitempty"`
	Subset   *Subset           `json:"subset,omitempty"`
}

type State struct {
	Handle        string
	Hash          string
	Definition    Definition
	Schema        map[string]ColumnSchema
	Snapshot      []Message
	Changes       []Message
	Materialized  map[string]Row
	CurrentOffset string
	LastAccess    time.Time
	Generation    uint64
	Deleted       bool
}

type ChangeMetadata struct {
	KeyRelation      Relation
	CommitLSN        uint64
	TransactionID    uint32
	DependentRefresh bool
}

type canonicalKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type canonicalSubset struct {
	Where       string        `json:"where,omitempty"`
	Params      []canonicalKV `json:"params,omitempty"`
	Limit       *int          `json:"limit,omitempty"`
	Offset      *int          `json:"offset,omitempty"`
	OrderBy     string        `json:"order_by,omitempty"`
	WhereExpr   string        `json:"where_expr,omitempty"`
	OrderByExpr string        `json:"order_by_expr,omitempty"`
}

type canonicalDefinition struct {
	Relation Relation         `json:"relation"`
	Where    string           `json:"where,omitempty"`
	Params   []canonicalKV    `json:"params,omitempty"`
	Columns  []string         `json:"columns,omitempty"`
	Replica  string           `json:"replica,omitempty"`
	Log      string           `json:"log,omitempty"`
	Subset   *canonicalSubset `json:"subset,omitempty"`
}

type persistedMessage struct {
	Headers  map[string]any `json:"headers"`
	Key      string         `json:"key,omitempty"`
	Value    Row            `json:"value,omitempty"`
	OldValue Row            `json:"old_value,omitempty"`
	Offset   string         `json:"offset,omitempty"`
}

type Manager struct {
	store      storage.Store
	mu         sync.RWMutex
	byHandle   map[string]State
	byHash     map[string]string
	byRelation map[string]map[string]struct{}
	generation uint64
	waiters    map[string][]chan struct{}
}

func NewManager(store storage.Store) (*Manager, error) {
	manager := &Manager{
		store:      store,
		byHandle:   map[string]State{},
		byHash:     map[string]string{},
		byRelation: map[string]map[string]struct{}{},
		waiters:    map[string][]chan struct{}{},
	}
	if err := manager.loadPersisted(context.Background()); err != nil {
		return nil, err
	}
	return manager, nil
}

var (
	ErrShapeNotFound    = errors.New("shape not found")
	ErrShapeDeleted     = errors.New("shape deleted")
	ErrOffsetOutOfRange = errors.New("offset out of range")
)

func (m *Manager) Canonicalize(def Definition) ([]byte, string) {
	canonical := canonicalDefinition{
		Relation: def.Relation,
		Where:    def.Where,
		Params:   canonicalizeMap(def.Params),
		Columns:  canonicalizeStrings(def.Columns),
		Replica:  def.Replica,
		Log:      def.Log,
	}

	if def.Subset != nil {
		canonical.Subset = &canonicalSubset{
			Where:       def.Subset.Where,
			Params:      canonicalizeMap(def.Subset.Params),
			Limit:       def.Subset.Limit,
			Offset:      def.Subset.Offset,
			OrderBy:     def.Subset.OrderBy,
			WhereExpr:   def.Subset.WhereExpr,
			OrderByExpr: def.Subset.OrderByExpr,
		}
	}

	bytes, _ := json.Marshal(canonical)
	sum := sha256.Sum256(bytes)
	return bytes, hex.EncodeToString(sum[:])
}

func (m *Manager) UpsertSnapshot(def Definition, snapshot SnapshotResult) (State, error) {
	return m.UpsertSnapshotAtOffset(def, snapshot, InitialOffset)
}

func (m *Manager) UpsertSnapshotAtOffset(def Definition, snapshot SnapshotResult, currentOffset string) (State, error) {
	_, hash := m.Canonicalize(def)
	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	if handle, ok := m.byHash[hash]; ok {
		existing := m.byHandle[handle]
		updated := existing
		updated.Definition = def
		updated.Schema = cloneSchema(snapshot.Schema)
		updated.Snapshot = buildSnapshotMessages(def, snapshot)
		decorateDependentSnapshotMessages(updated.Handle, updated.Definition, updated.Snapshot)
		updated.Changes = nil
		updated.Materialized = materializedRows(snapshot.Schema, snapshot.Rows)
		updated.CurrentOffset = currentOffset
		updated.LastAccess = now
		updated.Deleted = false
		if err := m.persistStateLocked(updated); err != nil {
			return State{}, err
		}

		m.removeRelationLocked(existing.Definition.Relation, handle)
		m.byHandle[handle] = updated
		m.byHash[hash] = handle
		m.addRelationLocked(def.Relation, handle)
		m.notifyLocked(handle)
		return updated, nil
	}

	m.generation++
	handle := hash[:10] + "-" + strconv.FormatInt(now.UnixMicro(), 10)
	state := State{
		Handle:        handle,
		Hash:          hash,
		Definition:    def,
		Schema:        cloneSchema(snapshot.Schema),
		Snapshot:      buildSnapshotMessages(def, snapshot),
		Materialized:  materializedRows(snapshot.Schema, snapshot.Rows),
		CurrentOffset: currentOffset,
		LastAccess:    now,
		Generation:    m.generation,
	}
	decorateDependentSnapshotMessages(state.Handle, state.Definition, state.Snapshot)
	if err := m.persistStateLocked(state); err != nil {
		m.generation--
		return State{}, err
	}

	m.byHandle[handle] = state
	m.byHash[hash] = handle
	m.addRelationLocked(def.Relation, handle)
	return state, nil
}

func (m *Manager) LookupOrCreateDefinition(def Definition) (State, error) {
	_, hash := m.Canonicalize(def)

	m.mu.Lock()
	defer m.mu.Unlock()

	if handle, ok := m.byHash[hash]; ok {
		state := m.byHandle[handle]
		state.LastAccess = time.Now().UTC()
		if err := m.persistStateLocked(state); err != nil {
			return State{}, err
		}
		m.byHandle[handle] = state
		return state, nil
	}

	m.generation++
	state := State{
		Handle:        hash[:10] + "-" + strconv.FormatInt(time.Now().UnixMicro(), 10),
		Hash:          hash,
		Definition:    def,
		Materialized:  map[string]Row{},
		CurrentOffset: InitialOffset,
		LastAccess:    time.Now().UTC(),
		Generation:    m.generation,
	}
	if err := m.persistStateLocked(state); err != nil {
		m.generation--
		return State{}, err
	}

	m.byHandle[state.Handle] = state
	m.byHash[hash] = state.Handle
	m.addRelationLocked(def.Relation, state.Handle)
	return state, nil
}

func (m *Manager) LookupByHandle(handle string) (State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.byHandle[handle]
	if !ok || state.Deleted {
		return State{}, false
	}
	return state, true
}

func (m *Manager) LookupByDefinition(def Definition) (State, bool) {
	_, hash := m.Canonicalize(def)

	m.mu.RLock()
	defer m.mu.RUnlock()

	handle, ok := m.byHash[hash]
	if !ok {
		return State{}, false
	}

	state, ok := m.byHandle[handle]
	if !ok || state.Deleted {
		return State{}, false
	}
	return state, true
}

func (m *Manager) Delete(handle string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return false, nil
	}

	updated := state
	updated.Deleted = true
	updated.LastAccess = time.Now().UTC()
	if err := m.persistStateLocked(updated); err != nil {
		return false, err
	}

	delete(m.byHash, state.Hash)
	m.removeRelationLocked(state.Definition.Relation, handle)
	m.byHandle[handle] = updated
	m.notifyLocked(handle)
	return true, nil
}

func (m *Manager) InvalidateByRelation(relation Relation) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handles := m.byRelation[relationKey(relation)]
	if len(handles) == 0 {
		return nil, nil
	}

	sortedHandles := make([]string, 0, len(handles))
	updatedStates := make(map[string]State, len(handles))
	for handle := range handles {
		sortedHandles = append(sortedHandles, handle)
	}
	sort.Strings(sortedHandles)

	for _, handle := range sortedHandles {
		state, ok := m.byHandle[handle]
		if !ok || state.Deleted {
			continue
		}
		updated := state
		updated.Deleted = true
		updated.LastAccess = time.Now().UTC()
		if err := m.persistStateLocked(updated); err != nil {
			return nil, err
		}
		updatedStates[handle] = updated
	}

	invalidated := make([]string, 0, len(updatedStates))
	for _, handle := range sortedHandles {
		updated, ok := updatedStates[handle]
		if !ok {
			continue
		}
		delete(m.byHash, updated.Hash)
		m.byHandle[handle] = updated
		invalidated = append(invalidated, handle)
		m.notifyLocked(handle)
	}
	delete(m.byRelation, relationKey(relation))
	return invalidated, nil
}

func (m *Manager) InvalidateAll() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handles := make([]string, 0, len(m.byHandle))
	for handle := range m.byHandle {
		handles = append(handles, handle)
	}
	sort.Strings(handles)

	invalidated := make([]string, 0, len(handles))
	for _, handle := range handles {
		state := m.byHandle[handle]
		if state.Deleted {
			continue
		}

		updated := state
		updated.Deleted = true
		updated.LastAccess = time.Now().UTC()
		if err := m.persistStateLocked(updated); err != nil {
			return invalidated, err
		}

		m.removeRelationLocked(state.Definition.Relation, handle)
		delete(m.byHash, updated.Hash)
		m.byHandle[handle] = updated
		invalidated = append(invalidated, handle)
		m.notifyLocked(handle)
	}
	return invalidated, nil
}

func (m *Manager) ActiveByRelation(relation Relation) []State {
	m.mu.RLock()
	defer m.mu.RUnlock()

	handles := m.byRelation[relationKey(relation)]
	if len(handles) == 0 {
		return nil
	}

	states := make([]State, 0, len(handles))
	for handle := range handles {
		state, ok := m.byHandle[handle]
		if !ok || state.Deleted {
			continue
		}
		states = append(states, state)
	}

	sort.Slice(states, func(i, j int) bool {
		return states[i].Handle < states[j].Handle
	})
	return states
}

func (m *Manager) ActiveStates() []State {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]State, 0, len(m.byHandle))
	for _, state := range m.byHandle {
		if state.Deleted {
			continue
		}
		states = append(states, state)
	}

	sort.Slice(states, func(i, j int) bool {
		return states[i].Handle < states[j].Handle
	})
	return states
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.byHandle)
}

func (m *Manager) Read(handle string, offset string) (State, []Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.readLocked(handle, offset)
}

func (m *Manager) WaitForChange(ctx context.Context, handle string, offset string) (State, []Message, error) {
	if state, messages, err := m.Read(handle, offset); err != nil || len(messages) > 0 || state.Deleted {
		return state, messages, err
	}

	waiter := make(chan struct{})

	m.mu.Lock()
	state, messages, err := m.readLocked(handle, offset)
	if err != nil || len(messages) > 0 || state.Deleted {
		m.mu.Unlock()
		return state, messages, err
	}
	m.waiters[handle] = append(m.waiters[handle], waiter)
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		m.removeWaiter(handle, waiter)
		return State{}, nil, ctx.Err()
	case <-waiter:
		return m.Read(handle, offset)
	}
}

func (m *Manager) Append(handle string, messages []Message) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, ErrShapeNotFound
	}
	if state.Deleted {
		return State{}, ErrShapeDeleted
	}

	updated := state
	updated.Materialized = cloneMaterialized(state.Materialized)
	for index, message := range messages {
		cloned := cloneMessage(message)
		if cloned.Offset == "" {
			cloned.Offset = NextGeneratedOffset(updated.CurrentOffset, index)
		}
		updated.Changes = append(updated.Changes, cloned)
		applyMessageToMaterialized(updated.Materialized, cloned)
		if CompareOffsets(cloned.Offset, updated.CurrentOffset) > 0 {
			updated.CurrentOffset = cloned.Offset
		}
	}
	updated.LastAccess = time.Now().UTC()
	if err := m.persistStateLocked(updated); err != nil {
		return State{}, err
	}

	m.byHandle[handle] = updated
	m.notifyLocked(handle)
	return updated, nil
}

func (m *Manager) Refresh(handle string, snapshot SnapshotResult) (State, []Message, error) {
	return m.RefreshWithMetadata(handle, snapshot, ChangeMetadata{})
}

func (m *Manager) RefreshWithMetadata(handle string, snapshot SnapshotResult, metadata ChangeMetadata) (State, []Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, nil, ErrShapeNotFound
	}
	if state.Deleted {
		return State{}, nil, ErrShapeDeleted
	}

	updated := state
	updated.Schema = cloneSchema(snapshot.Schema)
	updated.Materialized = materializedRows(snapshot.Schema, snapshot.Rows)
	updated.LastAccess = time.Now().UTC()

	messages := diffRows(updated.Definition, snapshot.Schema, state.Materialized, updated.Materialized, updated.Definition.Relation)
	if metadata.DependentRefresh {
		messages = decorateDependentRefreshMessages(updated.Handle, updated.Definition, messages, metadata)
	}
	if len(messages) > 0 {
		updated.CurrentOffset = applyChangeOffsets(updated.CurrentOffset, messages, metadata)
		for index := range messages {
			updated.Changes = append(updated.Changes, cloneMessage(messages[index]))
		}
	}

	if err := m.persistStateLocked(updated); err != nil {
		return State{}, nil, err
	}

	m.byHandle[handle] = updated
	if len(messages) > 0 {
		m.notifyLocked(handle)
		return updated, cloneMessages(messages), nil
	}
	return updated, nil, nil
}

func (m *Manager) RefreshKeys(handle string, snapshot SnapshotResult, keyRows []Row) (State, []Message, error) {
	return m.RefreshKeysWithMetadata(handle, snapshot, keyRows, ChangeMetadata{})
}

func (m *Manager) RefreshKeysWithMetadata(handle string, snapshot SnapshotResult, keyRows []Row, metadata ChangeMetadata) (State, []Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, nil, ErrShapeNotFound
	}
	if state.Deleted {
		return State{}, nil, ErrShapeDeleted
	}

	targetedKeys := targetedKeySet(snapshot.Schema, keyRows, snapshot.Rows)
	if len(targetedKeys) == 0 {
		return state, nil, nil
	}

	updated := state
	updated.Schema = cloneSchema(snapshot.Schema)
	updated.Materialized = cloneMaterialized(state.Materialized)
	updated.LastAccess = time.Now().UTC()

	currentRows := materializedRows(snapshot.Schema, snapshot.Rows)
	for key := range targetedKeys {
		delete(updated.Materialized, key)
	}
	for key, row := range currentRows {
		if _, ok := targetedKeys[key]; ok {
			updated.Materialized[key] = cloneRow(row)
		}
	}

	previousTargeted := subsetRows(state.Materialized, targetedKeys)
	currentTargeted := subsetRows(updated.Materialized, targetedKeys)
	keyRelation := metadata.KeyRelation
	if keyRelation == (Relation{}) {
		keyRelation = updated.Definition.Relation
	}

	messages := diffRows(updated.Definition, snapshot.Schema, previousTargeted, currentTargeted, keyRelation)
	if len(messages) > 0 {
		if metadata.CommitLSN > 0 {
			applyLiveWireFormat(snapshot.Schema, messages)
		}
		updated.CurrentOffset = applyChangeOffsets(updated.CurrentOffset, messages, metadata)
		for index := range messages {
			updated.Changes = append(updated.Changes, cloneMessage(messages[index]))
		}
	}

	if err := m.persistStateLocked(updated); err != nil {
		return State{}, nil, err
	}

	m.byHandle[handle] = updated
	if len(messages) > 0 {
		m.notifyLocked(handle)
		return updated, cloneMessages(messages), nil
	}
	return updated, nil, nil
}

func (m *Manager) readLocked(handle string, offset string) (State, []Message, error) {
	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, nil, ErrShapeNotFound
	}
	if state.Deleted {
		return state, nil, ErrShapeDeleted
	}

	if _, ok := ParseOffset(offset); !ok {
		return state, nil, ErrOffsetOutOfRange
	}
	comparison := CompareOffsets(offset, state.CurrentOffset)
	if comparison > 0 {
		return state, nil, ErrOffsetOutOfRange
	}
	if comparison == 0 {
		return state, nil, nil
	}

	filtered := make([]Message, 0, len(state.Changes))
	for _, message := range state.Changes {
		if CompareOffsets(message.Offset, offset) > 0 {
			filtered = append(filtered, cloneMessage(message))
		}
	}
	return state, filtered, nil
}

func (m *Manager) removeWaiter(handle string, waiter chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	waiters := m.waiters[handle]
	for index, candidate := range waiters {
		if candidate == waiter {
			m.waiters[handle] = append(waiters[:index], waiters[index+1:]...)
			break
		}
	}
	if len(m.waiters[handle]) == 0 {
		delete(m.waiters, handle)
	}
}

func (m *Manager) notifyLocked(handle string) {
	waiters := m.waiters[handle]
	if len(waiters) == 0 {
		return
	}

	delete(m.waiters, handle)
	for _, waiter := range waiters {
		close(waiter)
	}
}

func (m *Manager) loadPersisted(ctx context.Context) error {
	shapes, err := m.store.LoadShapes(ctx)
	if err != nil {
		return err
	}

	for _, persisted := range shapes {
		state, err := unmarshalState(persisted)
		if err != nil {
			state = State{
				Handle:        persisted.Handle,
				Hash:          persisted.Hash,
				CurrentOffset: persisted.CurrentOffset,
				LastAccess:    persisted.LastAccess.UTC(),
				Generation:    persisted.Generation,
				Deleted:       true,
			}
			if err := m.persistStateLocked(state); err != nil {
				return err
			}
		}
		m.byHandle[state.Handle] = state
		if !state.Deleted {
			m.byHash[state.Hash] = state.Handle
			m.addRelationLocked(state.Definition.Relation, state.Handle)
		}
		if state.Generation > m.generation {
			m.generation = state.Generation
		}
	}
	return nil
}

func (m *Manager) persistStateLocked(state State) error {
	persisted, err := marshalState(state)
	if err != nil {
		return err
	}
	return m.store.SaveShape(context.Background(), persisted)
}

func marshalState(state State) (storage.PersistedShape, error) {
	definition, err := json.Marshal(state.Definition)
	if err != nil {
		return storage.PersistedShape{}, err
	}
	schema, err := json.Marshal(state.Schema)
	if err != nil {
		return storage.PersistedShape{}, err
	}
	snapshot, err := json.Marshal(state.Snapshot)
	if err != nil {
		return storage.PersistedShape{}, err
	}
	materialized, err := json.Marshal(state.Materialized)
	if err != nil {
		return storage.PersistedShape{}, err
	}

	changes := make([]json.RawMessage, 0, len(state.Changes))
	for _, change := range state.Changes {
		encoded, err := json.Marshal(persistedMessage{
			Headers:  change.Headers,
			Key:      change.Key,
			Value:    change.Value,
			OldValue: change.OldValue,
			Offset:   change.Offset,
		})
		if err != nil {
			return storage.PersistedShape{}, err
		}
		changes = append(changes, encoded)
	}

	return storage.PersistedShape{
		Handle:        state.Handle,
		Hash:          state.Hash,
		Definition:    definition,
		Schema:        schema,
		Snapshot:      snapshot,
		Materialized:  materialized,
		CurrentOffset: state.CurrentOffset,
		LastAccess:    state.LastAccess.UTC(),
		Generation:    state.Generation,
		Deleted:       state.Deleted,
		Changes:       changes,
	}, nil
}

func unmarshalState(persisted storage.PersistedShape) (State, error) {
	var (
		definition   Definition
		schema       map[string]ColumnSchema
		snapshot     []Message
		materialized map[string]Row
		changes      []Message
	)

	if len(persisted.Definition) > 0 {
		if err := json.Unmarshal(persisted.Definition, &definition); err != nil {
			return State{}, err
		}
	}
	if len(persisted.Schema) > 0 {
		if err := json.Unmarshal(persisted.Schema, &schema); err != nil {
			return State{}, err
		}
	}
	if len(persisted.Snapshot) > 0 {
		if err := json.Unmarshal(persisted.Snapshot, &snapshot); err != nil {
			return State{}, err
		}
	}
	if len(persisted.Materialized) > 0 {
		if err := json.Unmarshal(persisted.Materialized, &materialized); err != nil {
			return State{}, err
		}
	}
	for _, raw := range persisted.Changes {
		var message persistedMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return State{}, err
		}
		changes = append(changes, Message{
			Headers:  message.Headers,
			Key:      message.Key,
			Value:    message.Value,
			OldValue: message.OldValue,
			Offset:   message.Offset,
		})
	}

	return State{
		Handle:        persisted.Handle,
		Hash:          persisted.Hash,
		Definition:    definition,
		Schema:        schema,
		Snapshot:      snapshot,
		Changes:       changes,
		Materialized:  materialized,
		CurrentOffset: persisted.CurrentOffset,
		LastAccess:    persisted.LastAccess.UTC(),
		Generation:    persisted.Generation,
		Deleted:       persisted.Deleted,
	}, nil
}

func buildSnapshotMessages(def Definition, snapshot SnapshotResult) []Message {
	if def.Log == "changes_only" {
		return nil
	}

	messages := make([]Message, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		internalKey := PrimaryKeySignature(snapshot.Schema, row)
		messages = append(messages, Message{
			Headers: map[string]any{
				"operation": "insert",
				"relation":  RelationHeader(def.Relation),
			},
			Key:         MessageKey(def.Relation, snapshot.Schema, row),
			Value:       cloneRow(row),
			InternalKey: internalKey,
		})
	}
	return messages
}

func definitionHasDependencyKeyword(def Definition) bool {
	if sqlinspect.ContainsDependencyKeyword(def.Where) {
		return true
	}
	if def.Subset == nil {
		return false
	}
	return sqlinspect.ContainsDependencyKeyword(def.Subset.Where) ||
		sqlinspect.ContainsDependencyKeyword(def.Subset.WhereExpr) ||
		sqlinspect.ContainsDependencyKeyword(def.Subset.OrderBy) ||
		sqlinspect.ContainsDependencyKeyword(def.Subset.OrderByExpr)
}

func decorateDependentSnapshotMessages(handle string, def Definition, messages []Message) {
	if !definitionHasDependencyKeyword(def) {
		return
	}
	for index := range messages {
		addDependentTag(handle, &messages[index])
	}
}

func materializedRows(schema map[string]ColumnSchema, rows []Row) map[string]Row {
	result := make(map[string]Row, len(rows))
	for _, row := range rows {
		result[PrimaryKeySignature(schema, row)] = cloneRow(row)
	}
	return result
}

func diffRows(def Definition, schema map[string]ColumnSchema, previous map[string]Row, current map[string]Row, keyRelation Relation) []Message {
	keys := make([]string, 0, len(previous)+len(current))
	seen := map[string]struct{}{}
	for key := range previous {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range current {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	messages := make([]Message, 0, len(keys))
	for _, key := range keys {
		oldRow, hadOld := previous[key]
		newRow, hasNew := current[key]

		switch {
		case !hadOld && hasNew:
			messages = append(messages, Message{
				Headers: map[string]any{
					"operation": "insert",
					"relation":  RelationHeader(def.Relation),
				},
				Key:         MessageKey(keyRelation, schema, newRow),
				Value:       cloneRow(newRow),
				InternalKey: key,
			})
		case hadOld && !hasNew:
			messages = append(messages, Message{
				Headers: map[string]any{
					"operation": "delete",
					"relation":  RelationHeader(def.Relation),
				},
				Key:         MessageKey(keyRelation, schema, oldRow),
				Value:       deleteValue(def, schema, oldRow),
				InternalKey: key,
			})
		case hadOld && hasNew && !reflect.DeepEqual(oldRow, newRow):
			value, oldValue, changed := updateValues(def, schema, oldRow, newRow)
			if !changed {
				continue
			}

			message := Message{
				Headers: map[string]any{
					"operation": "update",
					"relation":  RelationHeader(def.Relation),
				},
				Key:         MessageKey(keyRelation, schema, newRow),
				Value:       value,
				InternalKey: key,
			}
			if len(oldValue) > 0 {
				message.OldValue = oldValue
			}
			messages = append(messages, message)
		}
	}
	return messages
}

func decorateDependentRefreshMessages(handle string, def Definition, messages []Message, metadata ChangeMetadata) []Message {
	if !definitionHasDependencyKeyword(def) {
		return messages
	}

	relatedRelationChanged := metadata.KeyRelation != (Relation{}) && metadata.KeyRelation != def.Relation
	decorated := make([]Message, 0, len(messages)+1)
	moveIn := false
	for index := range messages {
		message := cloneMessage(messages[index])
		operation, _ := message.Headers["operation"].(string)
		if relatedRelationChanged && operation == "delete" {
			decorated = append(decorated, dependentMoveOutMessage(handle, message.InternalKey))
			continue
		}

		addDependentTag(handle, &message)
		if relatedRelationChanged && operation == "insert" {
			message.Headers["is_move_in"] = true
			moveIn = true
		}
		decorated = append(decorated, message)
	}

	if moveIn {
		decorated = append(decorated, Message{
			Headers: map[string]any{
				"control":  "snapshot-end",
				"xmin":     "0",
				"xmax":     "0",
				"xip_list": []string{},
			},
		})
	}
	return decorated
}

func addDependentTag(handle string, message *Message) {
	if message == nil || message.InternalKey == "" {
		return
	}
	if message.Headers == nil {
		message.Headers = map[string]any{}
	}
	message.Headers["tags"] = []string{dependentTag(handle, message.InternalKey)}
}

func dependentMoveOutMessage(handle string, internalKey string) Message {
	return Message{
		Headers: map[string]any{
			"event": "move-out",
			"patterns": []map[string]any{
				{
					"pos":   0,
					"value": dependentTag(handle, internalKey),
				},
			},
		},
		InternalKey: internalKey,
	}
}

func dependentTag(handle string, internalKey string) string {
	sum := md5.Sum([]byte(handle + "v:" + dependentTagValue(internalKey)))
	return hex.EncodeToString(sum[:])
}

func dependentTagValue(internalKey string) string {
	if internalKey == "" {
		return ""
	}
	if !strings.Contains(internalKey, "|") {
		if _, value, ok := strings.Cut(internalKey, "="); ok {
			return value
		}
	}
	return internalKey
}

func updateValues(def Definition, schema map[string]ColumnSchema, oldRow Row, newRow Row) (Row, Row, bool) {
	changedColumns := map[string]struct{}{}
	for name, value := range newRow {
		if !reflect.DeepEqual(oldRow[name], value) {
			changedColumns[name] = struct{}{}
		}
	}
	for name := range oldRow {
		if _, ok := newRow[name]; !ok {
			changedColumns[name] = struct{}{}
		}
	}
	if len(changedColumns) == 0 {
		return nil, nil, false
	}

	if def.Replica == "full" {
		oldValue := Row{}
		for name := range changedColumns {
			if value, ok := oldRow[name]; ok {
				oldValue[name] = value
			}
		}
		return cloneRow(newRow), oldValue, true
	}

	value := primaryKeyRow(schema, newRow)
	for name := range changedColumns {
		value[name] = newRow[name]
	}
	return value, nil, true
}

func deleteValue(def Definition, schema map[string]ColumnSchema, row Row) Row {
	if def.Replica == "full" {
		return cloneRow(row)
	}
	return primaryKeyRow(schema, row)
}

func primaryKeyRow(schema map[string]ColumnSchema, row Row) Row {
	projected := Row{}
	for name, column := range schema {
		if column.PKIndex == nil {
			continue
		}
		if value, ok := row[name]; ok {
			projected[name] = value
		}
	}
	return projected
}

func applyMessageToMaterialized(materialized map[string]Row, message Message) {
	if materialized == nil {
		return
	}

	operation, _ := message.Headers["operation"].(string)
	key := message.InternalKey
	if key == "" {
		key = message.Key
	}
	switch operation {
	case "delete":
		delete(materialized, key)
	case "insert", "update":
		materialized[key] = cloneRow(message.Value)
	}
}

func cloneSchema(schema map[string]ColumnSchema) map[string]ColumnSchema {
	if len(schema) == 0 {
		return nil
	}

	cloned := make(map[string]ColumnSchema, len(schema))
	for key, value := range schema {
		cloned[key] = value
	}
	return cloned
}

func cloneRow(row Row) Row {
	if len(row) == 0 {
		return nil
	}

	cloned := make(Row, len(row))
	for key, value := range row {
		cloned[key] = value
	}
	return cloned
}

func cloneMaterialized(materialized map[string]Row) map[string]Row {
	if len(materialized) == 0 {
		return map[string]Row{}
	}

	cloned := make(map[string]Row, len(materialized))
	for key, row := range materialized {
		cloned[key] = cloneRow(row)
	}
	return cloned
}

func cloneMessage(message Message) Message {
	cloned := Message{
		Headers:     map[string]any{},
		Key:         message.Key,
		Value:       cloneRow(message.Value),
		OldValue:    cloneRow(message.OldValue),
		Offset:      message.Offset,
		InternalKey: message.InternalKey,
	}
	for key, value := range message.Headers {
		cloned.Headers[key] = value
	}
	return cloned
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}

	cloned := make([]Message, 0, len(messages))
	for _, message := range messages {
		cloned = append(cloned, cloneMessage(message))
	}
	return cloned
}

func targetedKeySet(schema map[string]ColumnSchema, keyRows []Row, rows []Row) map[string]struct{} {
	targeted := map[string]struct{}{}
	for _, row := range keyRows {
		key := PrimaryKeySignature(schema, row)
		if key != "" {
			targeted[key] = struct{}{}
		}
	}
	for _, row := range rows {
		key := PrimaryKeySignature(schema, row)
		if key != "" {
			targeted[key] = struct{}{}
		}
	}
	return targeted
}

func subsetRows(rows map[string]Row, keys map[string]struct{}) map[string]Row {
	subset := map[string]Row{}
	for key := range keys {
		if row, ok := rows[key]; ok {
			subset[key] = cloneRow(row)
		}
	}
	return subset
}

func relationKey(relation Relation) string {
	return relation.Schema + "." + relation.Table
}

func (m *Manager) addRelationLocked(relation Relation, handle string) {
	key := relationKey(relation)
	if m.byRelation[key] == nil {
		m.byRelation[key] = map[string]struct{}{}
	}
	m.byRelation[key][handle] = struct{}{}
}

func (m *Manager) removeRelationLocked(relation Relation, handle string) {
	key := relationKey(relation)
	handles := m.byRelation[key]
	if len(handles) == 0 {
		return
	}
	delete(handles, handle)
	if len(handles) == 0 {
		delete(m.byRelation, key)
	}
}

func primaryKeyForRow(schema map[string]ColumnSchema, row Row) string {
	return PrimaryKeySignature(schema, row)
}

func canonicalizeMap(values map[string]string) []canonicalKV {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	items := make([]canonicalKV, 0, len(keys))
	for _, key := range keys {
		items = append(items, canonicalKV{Key: key, Value: values[key]})
	}
	return items
}

func canonicalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	cloned := append([]string(nil), values...)
	sort.Strings(cloned)
	return cloned
}

func applyChangeOffsets(currentOffset string, messages []Message, metadata ChangeMetadata) string {
	for index := range messages {
		if metadata.CommitLSN > 0 {
			lsn := strconv.FormatUint(metadata.CommitLSN, 10)
			messages[index].Offset = FormatLSNOffset(metadata.CommitLSN, index)
			if metadata.DependentRefresh {
				continue
			}
			messages[index].Headers["lsn"] = lsn
			messages[index].Headers["op_position"] = index
			messages[index].Headers["txids"] = []uint32{metadata.TransactionID}
		} else if messages[index].Offset == "" {
			messages[index].Offset = NextGeneratedOffset(currentOffset, index)
		}
	}

	if len(messages) == 0 {
		return currentOffset
	}
	if metadata.CommitLSN > 0 && !metadata.DependentRefresh {
		messages[len(messages)-1].Headers["last"] = true
	}
	return messages[len(messages)-1].Offset
}

func applyLiveWireFormat(schema map[string]ColumnSchema, messages []Message) {
	for index := range messages {
		applyLiveWireFormatRow(schema, messages[index].Value)
		applyLiveWireFormatRow(schema, messages[index].OldValue)
	}
}

func applyLiveWireFormatRow(schema map[string]ColumnSchema, row Row) {
	if len(row) == 0 {
		return
	}

	for name, value := range row {
		column, ok := schema[name]
		if !ok || column.Type != "bool" || value == nil {
			continue
		}

		asString, ok := value.(string)
		if !ok {
			continue
		}
		switch asString {
		case "true":
			row[name] = "t"
		case "false":
			row[name] = "f"
		}
	}
}

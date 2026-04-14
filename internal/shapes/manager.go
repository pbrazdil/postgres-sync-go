package shapes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

type Manager struct {
	store      storage.Store
	mu         sync.RWMutex
	byHandle   map[string]State
	byHash     map[string]string
	byRelation map[string]map[string]struct{}
	generation uint64
	waiters    map[string][]chan struct{}
}

func NewManager(store storage.Store) *Manager {
	return &Manager{
		store:      store,
		byHandle:   map[string]State{},
		byHash:     map[string]string{},
		byRelation: map[string]map[string]struct{}{},
		waiters:    map[string][]chan struct{}{},
	}
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

func (m *Manager) UpsertSnapshot(def Definition, snapshot SnapshotResult) State {
	_, hash := m.Canonicalize(def)

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()

	if handle, ok := m.byHash[hash]; ok {
		state := m.byHandle[handle]
		m.removeRelationLocked(state.Definition.Relation, handle)
		state.Definition = def
		state.Schema = cloneSchema(snapshot.Schema)
		state.Snapshot = buildSnapshotMessages(def, snapshot)
		state.Changes = nil
		state.Materialized = materializedRows(snapshot.Schema, snapshot.Rows)
		state.CurrentOffset = InitialOffset
		state.LastAccess = now
		state.Deleted = false
		m.byHandle[handle] = state
		m.addRelationLocked(def.Relation, handle)
		m.notifyLocked(handle)
		return state
	}

	m.generation++
	handle := hash[:10] + "-" + strconv.FormatInt(time.Now().UnixMicro(), 10)
	state := State{
		Handle:        handle,
		Hash:          hash,
		Definition:    def,
		Schema:        cloneSchema(snapshot.Schema),
		Snapshot:      buildSnapshotMessages(def, snapshot),
		Materialized:  materializedRows(snapshot.Schema, snapshot.Rows),
		CurrentOffset: InitialOffset,
		LastAccess:    now,
		Generation:    m.generation,
	}
	m.byHandle[handle] = state
	m.byHash[hash] = handle
	m.addRelationLocked(def.Relation, handle)

	return state
}

func (m *Manager) LookupOrCreateDefinition(def Definition) State {
	_, hash := m.Canonicalize(def)

	m.mu.Lock()
	defer m.mu.Unlock()

	if handle, ok := m.byHash[hash]; ok {
		state := m.byHandle[handle]
		state.LastAccess = time.Now().UTC()
		m.byHandle[handle] = state
		return state
	}

	m.generation++
	handle := hash[:10] + "-" + strconv.FormatInt(time.Now().UnixMicro(), 10)
	state := State{
		Handle:        handle,
		Hash:          hash,
		Definition:    def,
		Materialized:  map[string]Row{},
		CurrentOffset: InitialOffset,
		LastAccess:    time.Now().UTC(),
		Generation:    m.generation,
	}
	m.byHandle[handle] = state
	m.byHash[hash] = handle
	m.addRelationLocked(def.Relation, handle)

	return state
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

func (m *Manager) Delete(handle string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return false
	}

	delete(m.byHash, state.Hash)
	m.removeRelationLocked(state.Definition.Relation, handle)
	state.Deleted = true
	m.byHandle[handle] = state
	m.notifyLocked(handle)
	return true
}

func (m *Manager) InvalidateByRelation(relation Relation) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	handles := m.byRelation[relationKey(relation)]
	if len(handles) == 0 {
		return nil
	}

	invalidated := make([]string, 0, len(handles))
	for handle := range handles {
		state, ok := m.byHandle[handle]
		if !ok || state.Deleted {
			continue
		}

		delete(m.byHash, state.Hash)
		state.Deleted = true
		m.byHandle[handle] = state
		invalidated = append(invalidated, handle)
		m.notifyLocked(handle)
	}
	delete(m.byRelation, relationKey(relation))
	sort.Strings(invalidated)
	return invalidated
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

	sequence := offsetSequence(state.CurrentOffset)
	for _, message := range messages {
		sequence++
		message.Offset = formatOffset(sequence)
		state.Changes = append(state.Changes, message)
		applyMessageToMaterialized(state.Materialized, message)
	}
	state.CurrentOffset = formatOffset(sequence)
	state.LastAccess = time.Now().UTC()
	m.byHandle[handle] = state
	m.notifyLocked(handle)

	return state, nil
}

func (m *Manager) Refresh(handle string, snapshot SnapshotResult) (State, []Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, nil, ErrShapeNotFound
	}
	if state.Deleted {
		return State{}, nil, ErrShapeDeleted
	}

	updatedMaterialized := materializedRows(snapshot.Schema, snapshot.Rows)
	messages := diffRows(state.Definition, snapshot.Schema, state.Materialized, updatedMaterialized)

	state.Schema = cloneSchema(snapshot.Schema)
	state.Materialized = updatedMaterialized
	state.LastAccess = time.Now().UTC()

	if len(messages) > 0 {
		sequence := offsetSequence(state.CurrentOffset)
		for index := range messages {
			sequence++
			messages[index].Offset = formatOffset(sequence)
			state.Changes = append(state.Changes, messages[index])
		}
		state.CurrentOffset = formatOffset(sequence)
		m.byHandle[handle] = state
		m.notifyLocked(handle)
		return state, append([]Message(nil), messages...), nil
	}

	m.byHandle[handle] = state
	return state, nil, nil
}

func (m *Manager) readLocked(handle string, offset string) (State, []Message, error) {
	state, ok := m.byHandle[handle]
	if !ok {
		return State{}, nil, ErrShapeNotFound
	}
	if state.Deleted {
		return state, nil, ErrShapeDeleted
	}

	requested := offsetSequence(offset)
	current := offsetSequence(state.CurrentOffset)
	if requested > current {
		return state, nil, ErrOffsetOutOfRange
	}

	if requested == current {
		return state, nil, nil
	}

	if requested == 0 {
		messages := append([]Message(nil), state.Changes...)
		return state, messages, nil
	}

	start := requested
	if start < 0 {
		start = 0
	}
	if start > len(state.Changes) {
		return state, nil, ErrOffsetOutOfRange
	}

	messages := append([]Message(nil), state.Changes[start:]...)
	return state, messages, nil
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

func buildSnapshotMessages(def Definition, snapshot SnapshotResult) []Message {
	if def.Log == "changes_only" {
		return nil
	}

	messages := make([]Message, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		messages = append(messages, Message{
			Headers: map[string]any{"operation": "insert"},
			Key:     primaryKeyForRow(snapshot.Schema, row),
			Value:   cloneRow(row),
		})
	}

	return messages
}

func materializedRows(schema map[string]ColumnSchema, rows []Row) map[string]Row {
	result := make(map[string]Row, len(rows))
	for _, row := range rows {
		result[primaryKeyForRow(schema, row)] = cloneRow(row)
	}
	return result
}

func diffRows(def Definition, schema map[string]ColumnSchema, previous map[string]Row, current map[string]Row) []Message {
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
				Headers: map[string]any{"operation": "insert"},
				Key:     key,
				Value:   cloneRow(newRow),
			})
		case hadOld && !hasNew:
			messages = append(messages, Message{
				Headers: map[string]any{"operation": "delete"},
				Key:     key,
				Value:   deleteValue(def, schema, oldRow),
			})
		case hadOld && hasNew && !reflect.DeepEqual(oldRow, newRow):
			value, oldValue, changed := updateValues(def, schema, oldRow, newRow)
			if !changed {
				continue
			}

			message := Message{
				Headers: map[string]any{"operation": "update"},
				Key:     key,
				Value:   value,
			}
			if len(oldValue) > 0 {
				message.OldValue = oldValue
			}
			messages = append(messages, message)
		}
	}

	return messages
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
	switch operation {
	case "delete":
		delete(materialized, message.Key)
	case "insert", "update":
		materialized[message.Key] = cloneRow(message.Value)
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
	type pair struct {
		index int
		value string
	}

	pairs := make([]pair, 0, len(schema))
	for name, column := range schema {
		if column.PKIndex == nil {
			continue
		}

		value := ""
		if raw, ok := row[name]; ok && raw != nil {
			value = fmt.Sprint(raw)
		}

		pairs = append(pairs, pair{index: *column.PKIndex, value: value})
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

func offsetSequence(offset string) int {
	if offset == "" || offset == InitialOffset {
		return 0
	}
	parts := strings.SplitN(offset, "_", 2)
	if len(parts) != 2 {
		return 0
	}
	if parts[1] == "inf" {
		return int(^uint(0) >> 1)
	}
	value, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return value
}

func formatOffset(sequence int) string {
	return "0_" + strconv.Itoa(sequence)
}

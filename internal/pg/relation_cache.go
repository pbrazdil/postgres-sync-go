package pg

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"

	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

var (
	dependencyKeywordPattern  = regexp.MustCompile(`(?i)\b(select|join|exists|with|union|intersect|except)\b`)
	dependencyRelationPattern = regexp.MustCompile(`(?i)\b(from|join)\s+(?:only\s+)?((?:"[^"]+"|[a-zA-Z_][a-zA-Z0-9_$]*))(?:\s*\.\s*((?:"[^"]+"|[a-zA-Z_][a-zA-Z0-9_$]*)))?`)
)

type relationMetadata struct {
	Relation     shapes.Relation
	RootRelation shapes.Relation
	Columns      []describedColumn
	PKColumns    []describedColumn
}

func (r *Runtime) cacheRelationMetadata(ctx context.Context, relationID uint32, message *pglogrepl.RelationMessage) (relationMetadata, error) {
	relation := shapes.Relation{
		Schema: message.Namespace,
		Table:  message.RelationName,
	}

	pool, err := r.ensurePool(ctx)
	if err != nil {
		return relationMetadata{}, err
	}

	columns, err := describeRelation(ctx, pool, relation)
	if err != nil {
		var missing shapes.RelationNotFoundError
		if errors.As(err, &missing) {
			return relationMetadata{}, err
		}
		return relationMetadata{}, err
	}

	rootRelation, err := lookupPartitionRoot(ctx, pool, relation)
	if err != nil {
		return relationMetadata{}, err
	}

	pkColumns := primaryKeyColumns(columns)
	metadata := relationMetadata{
		Relation:     relation,
		RootRelation: rootRelation,
		Columns:      columns,
		PKColumns:    pkColumns,
	}

	r.mu.Lock()
	if r.relationCache == nil {
		r.relationCache = map[uint32]relationMetadata{}
	}
	r.relationCache[relationID] = metadata
	r.mu.Unlock()

	return metadata, nil
}

func (r *Runtime) relationMetadata(relationID uint32) (relationMetadata, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	metadata, ok := r.relationCache[relationID]
	return metadata, ok
}

func lookupPartitionRoot(ctx context.Context, pool relationQueryer, relation shapes.Relation) (shapes.Relation, error) {
	const query = `
WITH RECURSIVE ancestors AS (
	SELECT c.oid, c.relnamespace, c.relname, 0 AS depth
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = $1 AND c.relname = $2
	UNION ALL
	SELECT parent.oid, parent.relnamespace, parent.relname, ancestors.depth + 1
	FROM ancestors
	JOIN pg_inherits i ON i.inhrelid = ancestors.oid
	JOIN pg_class parent ON parent.oid = i.inhparent
)
SELECT n.nspname, c.relname
FROM ancestors
JOIN pg_class c ON c.oid = ancestors.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
ORDER BY ancestors.depth DESC
LIMIT 1`

	root := relation
	if err := pool.QueryRow(ctx, query, relation.Schema, relation.Table).Scan(&root.Schema, &root.Table); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return relation, nil
		}
		return shapes.Relation{}, err
	}
	return root, nil
}

func primaryKeyColumns(columns []describedColumn) []describedColumn {
	pkColumns := make([]describedColumn, 0, len(columns))
	for _, column := range columns {
		if column.PKIndex == nil {
			continue
		}
		pkColumns = append(pkColumns, column)
	}
	sort.Slice(pkColumns, func(i, j int) bool {
		return *pkColumns[i].PKIndex < *pkColumns[j].PKIndex
	})
	return pkColumns
}

func decodeTupleRow(columns []describedColumn, tuple *pglogrepl.TupleData) shapes.Row {
	if tuple == nil {
		return nil
	}

	row := shapes.Row{}
	for index, column := range columns {
		if index >= len(tuple.Columns) {
			break
		}
		cell := tuple.Columns[index]
		switch cell.DataType {
		case 'n':
			row[column.Name] = nil
		case 'u':
			row[column.Name] = "<unchanged-toast>"
		case 'b', 't':
			row[column.Name] = string(cell.Data)
		default:
			row[column.Name] = string(cell.Data)
		}
	}
	return row
}

func primaryKeyRowForColumns(pkColumns []describedColumn, row shapes.Row) shapes.Row {
	if len(pkColumns) == 0 || len(row) == 0 {
		return nil
	}

	key := shapes.Row{}
	for _, column := range pkColumns {
		key[column.Name] = row[column.Name]
	}
	return key
}

func targetedSnapshotColumns(metadata relationMetadata, explicit []string) []describedColumn {
	return projectedColumns(metadata.Columns, explicit)
}

func buildTargetedSnapshotQuery(def shapes.Definition, columns []describedColumn, pkColumns []describedColumn, keyRows []shapes.Row) (string, []any, error) {
	if len(pkColumns) == 0 {
		return "", nil, fmt.Errorf("relation %s.%s does not have a primary key", def.Relation.Schema, def.Relation.Table)
	}
	if len(keyRows) == 0 {
		return "", nil, errors.New("no changed primary keys to refresh")
	}

	selectParts := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted := quoteIdentifier(column.Name)
		selectParts = append(selectParts, fmt.Sprintf("%s::text AS %s", quoted, quoted))
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s.%s",
		strings.Join(selectParts, ", "),
		quoteIdentifier(def.Relation.Schema),
		quoteIdentifier(def.Relation.Table),
	)

	args := paramsAsArgs(def.Params)
	whereClauses := make([]string, 0, 2)
	if def.Where != "" {
		whereClauses = append(whereClauses, "("+def.Where+")")
	}

	targetedKeys := dedupeKeyRows(pkColumns, keyRows)
	keyClauses := make([]string, 0, len(targetedKeys))
	for _, keyRow := range targetedKeys {
		predicates := make([]string, 0, len(pkColumns))
		for _, column := range pkColumns {
			args = append(args, fmt.Sprint(keyRow[column.Name]))
			predicates = append(predicates, fmt.Sprintf("%s::text = $%d", quoteIdentifier(column.Name), len(args)))
		}
		keyClauses = append(keyClauses, "("+strings.Join(predicates, " AND ")+")")
	}
	whereClauses = append(whereClauses, "("+strings.Join(keyClauses, " OR ")+")")

	query += " WHERE " + strings.Join(whereClauses, " AND ")
	return query, args, nil
}

func dedupeKeyRows(pkColumns []describedColumn, keyRows []shapes.Row) []shapes.Row {
	if len(keyRows) == 0 {
		return nil
	}

	seen := map[string]shapes.Row{}
	for _, row := range keyRows {
		key := primaryKeySignature(primaryKeyRowForColumns(pkColumns, row))
		if key == "" {
			continue
		}
		seen[key] = primaryKeyRowForColumns(pkColumns, row)
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]shapes.Row, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, seen[key])
	}
	return rows
}

func definitionSupportsTargetedRefresh(def shapes.Definition) bool {
	if def.Subset != nil {
		return false
	}

	for _, clause := range definitionDependencyClauses(def) {
		sanitized := sanitizeDependencyClause(clause)
		if strings.TrimSpace(sanitized) == "" {
			continue
		}
		if dependencyKeywordPattern.MatchString(sanitized) {
			return false
		}
	}
	return true
}

type liveDependencySet struct {
	Unsupported bool
	Wildcard    bool
	Relations   []shapes.Relation
}

func definitionRequiresInvalidationForRelation(def shapes.Definition, relation shapes.Relation, rootRelation shapes.Relation) bool {
	dependencies := liveDependenciesForDefinition(def)
	if !dependencies.Unsupported {
		return false
	}
	if dependencies.Wildcard {
		return true
	}

	for _, candidate := range dependencies.Relations {
		if candidate == relation || candidate == rootRelation {
			return true
		}
	}
	return false
}

func liveDependenciesForDefinition(def shapes.Definition) liveDependencySet {
	if def.Subset != nil {
		return liveDependencySet{
			Unsupported: true,
			Relations:   []shapes.Relation{def.Relation},
		}
	}

	relations := map[string]shapes.Relation{}
	unsupported := false
	wildcard := false

	for _, clause := range definitionDependencyClauses(def) {
		sanitized := sanitizeDependencyClause(clause)
		if strings.TrimSpace(sanitized) == "" {
			continue
		}
		if !dependencyKeywordPattern.MatchString(sanitized) {
			continue
		}

		unsupported = true
		matches := dependencyRelationPattern.FindAllStringSubmatch(sanitized, -1)
		if len(matches) == 0 {
			wildcard = true
			continue
		}

		for _, match := range matches {
			relation, ok := parseDependencyRelation(match[2], match[3], def.Relation.Schema)
			if !ok {
				wildcard = true
				continue
			}
			relations[dependencyRelationKey(relation)] = relation
		}
	}

	if !unsupported {
		return liveDependencySet{}
	}

	relations[dependencyRelationKey(def.Relation)] = def.Relation
	keys := make([]string, 0, len(relations))
	for key := range relations {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]shapes.Relation, 0, len(keys))
	for _, key := range keys {
		result = append(result, relations[key])
	}

	return liveDependencySet{
		Unsupported: true,
		Wildcard:    wildcard,
		Relations:   result,
	}
}

func definitionDependencyClauses(def shapes.Definition) []string {
	clauses := []string{def.Where}
	if def.Subset != nil {
		clauses = append(clauses, def.Subset.Where, def.Subset.WhereExpr, def.Subset.OrderByExpr)
	}
	return clauses
}

func sanitizeDependencyClause(value string) string {
	if value == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(value))

	inSingleQuoted := false
	inLineComment := false
	inBlockComment := false

	for index := 0; index < len(value); index++ {
		switch {
		case inLineComment:
			if value[index] == '\n' {
				inLineComment = false
				builder.WriteByte(' ')
			}
			continue
		case inBlockComment:
			if value[index] == '*' && index+1 < len(value) && value[index+1] == '/' {
				inBlockComment = false
				index++
				builder.WriteByte(' ')
			}
			continue
		case inSingleQuoted:
			if value[index] == '\'' {
				if index+1 < len(value) && value[index+1] == '\'' {
					index++
					continue
				}
				inSingleQuoted = false
				builder.WriteByte(' ')
			}
			continue
		}

		if value[index] == '\'' {
			inSingleQuoted = true
			builder.WriteByte(' ')
			continue
		}
		if value[index] == '-' && index+1 < len(value) && value[index+1] == '-' {
			inLineComment = true
			index++
			builder.WriteByte(' ')
			continue
		}
		if value[index] == '/' && index+1 < len(value) && value[index+1] == '*' {
			inBlockComment = true
			index++
			builder.WriteByte(' ')
			continue
		}

		builder.WriteByte(value[index])
	}

	return builder.String()
}

func parseDependencyRelation(first string, second string, defaultSchema string) (shapes.Relation, bool) {
	left, ok := normalizeDependencyIdentifier(first)
	if !ok {
		return shapes.Relation{}, false
	}
	if second == "" {
		return shapes.Relation{Schema: defaultSchema, Table: left}, true
	}

	right, ok := normalizeDependencyIdentifier(second)
	if !ok {
		return shapes.Relation{}, false
	}
	return shapes.Relation{Schema: left, Table: right}, true
}

func normalizeDependencyIdentifier(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		if !strings.HasSuffix(trimmed, `"`) || len(trimmed) < 2 {
			return "", false
		}
		return strings.ReplaceAll(trimmed[1:len(trimmed)-1], `""`, `"`), true
	}
	return strings.ToLower(trimmed), true
}

func dependencyRelationKey(relation shapes.Relation) string {
	return relation.Schema + "." + relation.Table
}

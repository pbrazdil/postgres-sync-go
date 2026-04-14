package shapes

import (
	"fmt"
	"sort"
	"strings"
)

const internalKeySeparator = "\x1f"

func RelationHeader(relation Relation) []string {
	return []string{relation.Schema, relation.Table}
}

func MessageKey(relation Relation, schema map[string]ColumnSchema, row Row) string {
	values := primaryKeyValues(schema, row)
	if len(values) == 0 {
		return ""
	}

	parts := make([]string, 0, len(values)+1)
	parts = append(parts, quoteQualifiedRelation(relation))
	for _, value := range values {
		parts = append(parts, quoteKeyValue(value))
	}
	return strings.Join(parts, "/")
}

func PrimaryKeySignature(schema map[string]ColumnSchema, row Row) string {
	values := primaryKeyValues(schema, row)
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, internalKeySeparator)
}

func quoteQualifiedRelation(relation Relation) string {
	return fmt.Sprintf("%s.%s", quoteIdentifier(relation.Schema), quoteIdentifier(relation.Table))
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteKeyValue(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func primaryKeyValues(schema map[string]ColumnSchema, row Row) []string {
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
		return nil
	}

	values := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		values = append(values, pair.value)
	}
	return values
}

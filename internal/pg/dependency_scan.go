package pg

import (
	"sort"
	"strings"

	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
)

type dependencyRelationScan struct {
	Relations []shapes.Relation
	Wildcard  bool
}

type dependencyToken struct {
	Value  string
	Quoted bool
	Symbol bool
}

func scanDependencyRelations(clause string, defaultSchema string) dependencyRelationScan {
	tokens := tokenizeDependencyClause(clause)
	if len(tokens) == 0 {
		return dependencyRelationScan{}
	}

	relations := map[string]shapes.Relation{}
	ctes := collectDependencyCTENames(tokens)
	wildcard := scanDependencyTokens(tokens, defaultSchema, ctes, relations)

	keys := make([]string, 0, len(relations))
	for key := range relations {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]shapes.Relation, 0, len(keys))
	for _, key := range keys {
		result = append(result, relations[key])
	}

	return dependencyRelationScan{
		Relations: result,
		Wildcard:  wildcard,
	}
}

func tokenizeDependencyClause(clause string) []dependencyToken {
	tokens := []dependencyToken{}
	for index := 0; index < len(clause); {
		ch := clause[index]
		if isDependencySpace(ch) {
			index++
			continue
		}

		if ch == '"' {
			value, next, ok := readQuotedDependencyIdentifier(clause, index)
			if !ok {
				tokens = append(tokens, dependencyToken{Value: string(ch), Symbol: true})
				index++
				continue
			}
			tokens = append(tokens, dependencyToken{Value: value, Quoted: true})
			index = next
			continue
		}

		if isDependencyWordStart(ch) {
			start := index
			index++
			for index < len(clause) && isDependencyWordPart(clause[index]) {
				index++
			}
			tokens = append(tokens, dependencyToken{Value: clause[start:index]})
			continue
		}

		if isDependencySymbol(ch) {
			tokens = append(tokens, dependencyToken{Value: string(ch), Symbol: true})
		}
		index++
	}
	return tokens
}

func readQuotedDependencyIdentifier(value string, start int) (string, int, bool) {
	var builder strings.Builder
	for index := start + 1; index < len(value); index++ {
		if value[index] == '"' {
			if index+1 < len(value) && value[index+1] == '"' {
				builder.WriteByte('"')
				index++
				continue
			}
			return builder.String(), index + 1, true
		}
		builder.WriteByte(value[index])
	}
	return "", start, false
}

func isDependencySpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f'
}

func isDependencyWordStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '$'
}

func isDependencyWordPart(ch byte) bool {
	return isDependencyWordStart(ch) || (ch >= '0' && ch <= '9')
}

func isDependencySymbol(ch byte) bool {
	switch ch {
	case '(', ')', ',', '.':
		return true
	default:
		return false
	}
}

func collectDependencyCTENames(tokens []dependencyToken) map[string]struct{} {
	ctes := map[string]struct{}{}
	for index := 0; index < len(tokens); index++ {
		if !dependencyTokenKeyword(tokens[index], "with") {
			continue
		}

		index++
		if index < len(tokens) && dependencyTokenKeyword(tokens[index], "recursive") {
			index++
		}

		for index < len(tokens) {
			if !dependencyTokenIdentifier(tokens[index]) {
				break
			}
			ctes[dependencyTokenName(tokens[index])] = struct{}{}
			index++

			if index < len(tokens) && dependencyTokenValue(tokens[index], "(") {
				index = skipDependencyBalanced(tokens, index)
			}
			if index < len(tokens) && dependencyTokenKeyword(tokens[index], "as") {
				index++
			}
			if index < len(tokens) && dependencyTokenValue(tokens[index], "(") {
				index = skipDependencyBalanced(tokens, index)
			}
			if index < len(tokens) && dependencyTokenValue(tokens[index], ",") {
				index++
				continue
			}
			break
		}
	}
	return ctes
}

func scanDependencyTokens(tokens []dependencyToken, defaultSchema string, ctes map[string]struct{}, relations map[string]shapes.Relation) bool {
	wildcard := false
	depth := 0
	inFromList := false
	fromDepth := 0
	expectRelation := false

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if expectRelation {
			next, found, unknown := consumeDependencyRelation(tokens, index, defaultSchema, ctes, relations)
			if unknown {
				wildcard = true
			}
			expectRelation = false
			if next > index {
				index = next - 1
			} else if !found {
				continue
			}
			continue
		}

		if dependencyTokenValue(token, "(") {
			depth++
			continue
		}
		if dependencyTokenValue(token, ")") {
			if inFromList && depth <= fromDepth {
				inFromList = false
			}
			if depth > 0 {
				depth--
			}
			continue
		}

		if inFromList && depth <= fromDepth && dependencyFromBoundary(token) {
			inFromList = false
		}

		switch {
		case dependencyTokenKeyword(token, "from"):
			inFromList = true
			fromDepth = depth
			expectRelation = true
		case dependencyTokenKeyword(token, "join"):
			expectRelation = true
		case inFromList && depth == fromDepth && dependencyTokenValue(token, ","):
			expectRelation = true
		}
	}

	return wildcard
}

func consumeDependencyRelation(tokens []dependencyToken, index int, defaultSchema string, ctes map[string]struct{}, relations map[string]shapes.Relation) (int, bool, bool) {
	for index < len(tokens) && (dependencyTokenKeyword(tokens[index], "only") || dependencyTokenKeyword(tokens[index], "lateral")) {
		index++
	}
	if index >= len(tokens) {
		return index, false, true
	}

	if dependencyTokenValue(tokens[index], "(") {
		closeIndex := findDependencyBalancedClose(tokens, index)
		if closeIndex < 0 {
			return index + 1, false, true
		}

		inner := tokens[index+1 : closeIndex]
		if relation, ok := dependencyRelationFromTokens(inner, defaultSchema, ctes); ok {
			relations[dependencyRelationKey(relation)] = relation
			return skipDependencyAlias(tokens, closeIndex+1), true, false
		}

		unknown := scanDependencyTokens(inner, defaultSchema, ctes, relations)
		return skipDependencyAlias(tokens, closeIndex+1), false, unknown
	}

	relation, next, ok := readDependencyRelationReference(tokens, index, defaultSchema, ctes)
	if !ok {
		return index + 1, false, true
	}

	if next < len(tokens) && dependencyTokenValue(tokens[next], "(") {
		closeIndex := findDependencyBalancedClose(tokens, next)
		if closeIndex < 0 {
			return next + 1, false, true
		}
		unknown := scanDependencyTokens(tokens[next+1:closeIndex], defaultSchema, ctes, relations)
		return skipDependencyAlias(tokens, closeIndex+1), false, unknown
	}

	if relation != (shapes.Relation{}) {
		relations[dependencyRelationKey(relation)] = relation
	}
	return skipDependencyAlias(tokens, next), relation != (shapes.Relation{}), false
}

func dependencyRelationFromTokens(tokens []dependencyToken, defaultSchema string, ctes map[string]struct{}) (shapes.Relation, bool) {
	if len(tokens) == 0 {
		return shapes.Relation{}, false
	}
	relation, next, ok := readDependencyRelationReference(tokens, 0, defaultSchema, ctes)
	if !ok || relation == (shapes.Relation{}) {
		return shapes.Relation{}, false
	}
	return relation, next == len(tokens)
}

func readDependencyRelationReference(tokens []dependencyToken, index int, defaultSchema string, ctes map[string]struct{}) (shapes.Relation, int, bool) {
	if index >= len(tokens) || !dependencyTokenIdentifier(tokens[index]) {
		return shapes.Relation{}, index, false
	}

	first := dependencyTokenName(tokens[index])
	next := index + 1
	if next+1 < len(tokens) && dependencyTokenValue(tokens[next], ".") && dependencyTokenIdentifier(tokens[next+1]) {
		second := dependencyTokenName(tokens[next+1])
		return shapes.Relation{Schema: first, Table: second}, next + 2, true
	}

	if _, ok := ctes[first]; ok {
		return shapes.Relation{}, next, true
	}
	return shapes.Relation{Schema: defaultSchema, Table: first}, next, true
}

func skipDependencyAlias(tokens []dependencyToken, index int) int {
	if index < len(tokens) && dependencyTokenKeyword(tokens[index], "as") {
		index++
		if index < len(tokens) && dependencyTokenIdentifier(tokens[index]) {
			index++
		}
		if index < len(tokens) && dependencyTokenValue(tokens[index], "(") {
			index = skipDependencyBalanced(tokens, index)
		}
		return index
	}

	if index < len(tokens) && dependencyTokenIdentifier(tokens[index]) && !dependencyAliasBoundary(tokens[index]) {
		index++
		if index < len(tokens) && dependencyTokenValue(tokens[index], "(") {
			index = skipDependencyBalanced(tokens, index)
		}
	}
	return index
}

func skipDependencyBalanced(tokens []dependencyToken, index int) int {
	closeIndex := findDependencyBalancedClose(tokens, index)
	if closeIndex < 0 {
		return index + 1
	}
	return closeIndex + 1
}

func findDependencyBalancedClose(tokens []dependencyToken, index int) int {
	if index >= len(tokens) || !dependencyTokenValue(tokens[index], "(") {
		return -1
	}
	depth := 0
	for cursor := index; cursor < len(tokens); cursor++ {
		switch {
		case dependencyTokenValue(tokens[cursor], "("):
			depth++
		case dependencyTokenValue(tokens[cursor], ")"):
			depth--
			if depth == 0 {
				return cursor
			}
		}
	}
	return -1
}

func dependencyTokenIdentifier(token dependencyToken) bool {
	return !token.Symbol && token.Value != ""
}

func dependencyTokenKeyword(token dependencyToken, keyword string) bool {
	return dependencyTokenIdentifier(token) && !token.Quoted && strings.EqualFold(token.Value, keyword)
}

func dependencyTokenValue(token dependencyToken, value string) bool {
	return token.Symbol && token.Value == value
}

func dependencyTokenName(token dependencyToken) string {
	if token.Quoted {
		return token.Value
	}
	return strings.ToLower(token.Value)
}

func dependencyFromBoundary(token dependencyToken) bool {
	if !dependencyTokenIdentifier(token) || token.Quoted {
		return false
	}
	switch strings.ToLower(token.Value) {
	case "where", "group", "having", "order", "limit", "offset", "union", "intersect", "except", "window", "returning", "qualify":
		return true
	default:
		return false
	}
}

func dependencyAliasBoundary(token dependencyToken) bool {
	if token.Symbol {
		return token.Value == "," || token.Value == ")" || token.Value == "."
	}
	switch strings.ToLower(token.Value) {
	case "on", "using", "where", "join", "inner", "left", "right", "full", "cross", "natural", "group", "having", "order", "limit", "offset", "union", "intersect", "except", "window", "returning", "qualify":
		return true
	default:
		return false
	}
}

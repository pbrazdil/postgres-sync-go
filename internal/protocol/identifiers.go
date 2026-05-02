package protocol

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
)

func ParseRelation(input string) (shapes.Relation, error) {
	parts, err := parseSeparatedIdentifiers(input, '.')
	if err != nil {
		return shapes.Relation{}, err
	}

	switch len(parts) {
	case 1:
		return shapes.Relation{Schema: "public", Table: parts[0]}, nil
	case 2:
		return shapes.Relation{Schema: parts[0], Table: parts[1]}, nil
	default:
		return shapes.Relation{}, fmt.Errorf("Invalid qualified identifier: %q", input)
	}
}

func ParseColumns(input string) ([]string, error) {
	return parseSeparatedIdentifiers(input, ',')
}

func parseSeparatedIdentifiers(input string, separator rune) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("Invalid zero-length delimited identifier")
	}

	var (
		parts       []string
		buf         strings.Builder
		inQuotes    bool
		quotedToken bool
	)

	flush := func() error {
		token := strings.TrimSpace(buf.String())
		if token == "" {
			return fmt.Errorf("Invalid zero-length delimited identifier")
		}

		if !quotedToken {
			token = strings.ToLower(token)
		}

		parts = append(parts, token)
		buf.Reset()
		quotedToken = false
		return nil
	}

	for i := 0; i < len(input); i++ {
		ch := rune(input[i])

		switch {
		case ch == '"':
			if inQuotes && i+1 < len(input) && input[i+1] == '"' {
				buf.WriteByte('"')
				i++
				continue
			}

			if !inQuotes && strings.TrimSpace(buf.String()) == "" {
				quotedToken = true
			}

			inQuotes = !inQuotes
		case ch == separator && !inQuotes:
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			if !inQuotes && ch != '_' && ch != '$' && !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && !unicode.IsSpace(ch) {
				return nil, fmt.Errorf("Invalid unquoted identifier contains special characters: %q", string(ch))
			}

			buf.WriteRune(ch)
		}
	}

	if inQuotes {
		return nil, fmt.Errorf("Unterminated quoted identifier")
	}

	if err := flush(); err != nil {
		return nil, err
	}

	return parts, nil
}

package sqlinspect

import (
	"regexp"
	"strings"
)

var dependencyKeywordPattern = regexp.MustCompile(`(?i)\b(select|join|exists|with|union|intersect|except)\b`)

func ContainsDependencyKeyword(value string) bool {
	sanitized := SanitizeClause(value)
	return strings.TrimSpace(sanitized) != "" && dependencyKeywordPattern.MatchString(sanitized)
}

func SanitizeClause(value string) string {
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

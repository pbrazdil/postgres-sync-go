package shapes

import (
	"fmt"
	"strconv"
	"strings"
)

type offsetKind int

const (
	offsetInvalid offsetKind = iota
	offsetSequence
	offsetNow
	offsetLSN
)

type parsedOffset struct {
	raw   string
	kind  offsetKind
	seq   int64
	lsn   uint64
	opPos int64
}

func ParseOffset(raw string) (parsedOffset, bool) {
	if raw == "" {
		raw = InitialOffset
	}

	parts := strings.SplitN(raw, "_", 2)
	if len(parts) != 2 {
		return parsedOffset{raw: raw, kind: offsetInvalid}, false
	}

	if parts[0] == "0" {
		if parts[1] == "inf" {
			return parsedOffset{raw: raw, kind: offsetNow}, true
		}

		seq, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || seq < 0 {
			return parsedOffset{raw: raw, kind: offsetInvalid}, false
		}
		return parsedOffset{raw: raw, kind: offsetSequence, seq: seq}, true
	}

	lsn, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return parsedOffset{raw: raw, kind: offsetInvalid}, false
	}
	opPos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || opPos < 0 {
		return parsedOffset{raw: raw, kind: offsetInvalid}, false
	}
	return parsedOffset{raw: raw, kind: offsetLSN, lsn: lsn, opPos: opPos}, true
}

func CompareOffsets(left string, right string) int {
	parsedLeft, okLeft := ParseOffset(left)
	parsedRight, okRight := ParseOffset(right)
	switch {
	case !okLeft && !okRight:
		return 0
	case !okLeft:
		return -1
	case !okRight:
		return 1
	default:
		return compareParsedOffsets(parsedLeft, parsedRight)
	}
}

func compareParsedOffsets(left parsedOffset, right parsedOffset) int {
	leftRank := offsetRank(left.kind)
	rightRank := offsetRank(right.kind)
	if leftRank != rightRank {
		if leftRank < rightRank {
			return -1
		}
		return 1
	}

	switch left.kind {
	case offsetSequence:
		return compareInt64(left.seq, right.seq)
	case offsetNow:
		return 0
	case offsetLSN:
		if left.lsn != right.lsn {
			if left.lsn < right.lsn {
				return -1
			}
			return 1
		}
		return compareInt64(left.opPos, right.opPos)
	default:
		return 0
	}
}

func compareInt64(left int64, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func offsetRank(kind offsetKind) int {
	switch kind {
	case offsetSequence:
		return 1
	case offsetNow:
		return 2
	case offsetLSN:
		return 3
	default:
		return 0
	}
}

func FormatSequenceOffset(sequence int) string {
	return fmt.Sprintf("0_%d", sequence)
}

func FormatLSNOffset(lsn uint64, opPos int) string {
	return fmt.Sprintf("%d_%d", lsn, opPos)
}

func NextGeneratedOffset(current string, step int) string {
	parsed, ok := ParseOffset(current)
	if !ok {
		return FormatSequenceOffset(step + 1)
	}

	switch parsed.kind {
	case offsetLSN:
		return FormatLSNOffset(parsed.lsn, int(parsed.opPos)+step+1)
	case offsetNow:
		return FormatLSNOffset(uint64(step+1), 0)
	default:
		return FormatSequenceOffset(int(parsed.seq) + step + 1)
	}
}

func OffsetLSN(offset string) (string, bool) {
	parsed, ok := ParseOffset(offset)
	if !ok || parsed.kind != offsetLSN {
		return "", false
	}
	return strconv.FormatUint(parsed.lsn, 10), true
}

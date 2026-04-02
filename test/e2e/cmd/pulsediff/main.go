package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

type normalizedResponse struct {
	Status  int            `json:"status"`
	Headers map[string]any `json:"headers,omitempty"`
	Body    any            `json:"body,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "normalize-http":
		if err := runNormalizeHTTP(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "normalize-http: %v\n", err)
			os.Exit(1)
		}
	case "extract-header":
		if err := runExtractHeader(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "extract-header: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  pulsediff normalize-http --headers <path> --body <path>")
	fmt.Fprintln(os.Stderr, "  pulsediff extract-header --headers <path> --name <header>")
}

func runNormalizeHTTP(args []string) error {
	fs := flag.NewFlagSet("normalize-http", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	headersPath := fs.String("headers", "", "curl --dump-header output")
	bodyPath := fs.String("body", "", "curl body output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *headersPath == "" || *bodyPath == "" {
		return errors.New("both --headers and --body are required")
	}

	status, headers, err := parseHeaderFile(*headersPath)
	if err != nil {
		return err
	}

	bodyBytes, err := os.ReadFile(*bodyPath)
	if err != nil {
		return err
	}

	resp := normalizedResponse{
		Status:  status,
		Headers: normalizeHeaders(headers),
		Body:    normalizeBody(headerValue(headers, "content-type"), bodyBytes),
	}

	encoded, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", encoded)
	return nil
}

func runExtractHeader(args []string) error {
	fs := flag.NewFlagSet("extract-header", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	headersPath := fs.String("headers", "", "curl --dump-header output")
	name := fs.String("name", "", "header name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *headersPath == "" || *name == "" {
		return errors.New("both --headers and --name are required")
	}

	_, headers, err := parseHeaderFile(*headersPath)
	if err != nil {
		return err
	}

	fmt.Print(headerValue(headers, *name))
	return nil
}

func parseHeaderFile(path string) (int, map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	status := 0
	headers := map[string][]string{}
	seenFinal := false

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "HTTP/") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, nil, fmt.Errorf("invalid status line %q", line)
			}
			parsed, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0, nil, err
			}
			status = parsed
			headers = map[string][]string{}
			seenFinal = true
			continue
		}

		if line == "" {
			continue
		}
		if !seenFinal {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(key))
		headers[name] = append(headers[name], strings.TrimSpace(value))
	}

	if err := scanner.Err(); err != nil {
		return 0, nil, err
	}
	if status == 0 {
		return 0, nil, errors.New("no HTTP status found in headers file")
	}

	return status, headers, nil
}

func headerValue(headers map[string][]string, name string) string {
	values := headers[strings.ToLower(name)]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func normalizeHeaders(headers map[string][]string) map[string]any {
	keep := []string{
		"cache-control",
		"content-type",
		"electric-cursor",
		"electric-handle",
		"electric-has-data",
		"electric-internal-known-error",
		"electric-offset",
		"electric-schema",
		"electric-snapshot",
		"electric-up-to-date",
		"etag",
		"retry-after",
	}

	out := map[string]any{}
	for _, key := range keep {
		values := headers[key]
		if len(values) == 0 {
			continue
		}

		switch key {
		case "electric-handle":
			out[key] = "<handle>"
		case "electric-cursor":
			out[key] = "<cursor>"
		case "etag":
			out[key] = "<etag>"
		case "electric-offset":
			out[key] = normalizeOffset(values[len(values)-1])
		case "electric-up-to-date":
			out[key] = true
		case "electric-schema":
			var schema any
			if err := json.Unmarshal([]byte(values[len(values)-1]), &schema); err == nil {
				out[key] = normalizeDynamic(schema)
			} else {
				out[key] = values[len(values)-1]
			}
		case "retry-after":
			out[key] = "<retry-after>"
		default:
			if len(values) == 1 {
				out[key] = values[0]
			} else {
				copied := append([]string(nil), values...)
				sort.Strings(copied)
				out[key] = copied
			}
		}
	}
	return out
}

func normalizeBody(contentType string, body []byte) any {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}

	lowerContentType := strings.ToLower(contentType)
	if strings.Contains(lowerContentType, "text/event-stream") || bytes.Contains(trimmed, []byte("\ndata: ")) || bytes.HasPrefix(trimmed, []byte("data: ")) {
		return parseSSE(trimmed)
	}

	var decoded any
	if json.Unmarshal(trimmed, &decoded) == nil {
		return normalizeDynamic(decoded)
	}

	return string(trimmed)
}

func parseSSE(body []byte) any {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	events := make([]any, 0)
	dataLines := make([]string, 0)

	flushEvent := func() {
		if len(dataLines) == 0 {
			return
		}

		payload := strings.Join(dataLines, "\n")
		var decoded any
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			events = append(events, map[string]any{"data": normalizeDynamic(decoded)})
		} else {
			events = append(events, map[string]any{"data": payload})
		}
		dataLines = dataLines[:0]
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			flushEvent()
		case strings.HasPrefix(line, ":"):
			flushEvent()
			events = append(events, map[string]any{"comment": strings.TrimSpace(strings.TrimPrefix(line, ":"))})
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flushEvent()

	if len(events) == 0 {
		return string(bytes.TrimSpace(body))
	}
	return events
}

func normalizeDynamic(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, inner := range typed {
			switch key {
			case "lsn", "global_last_seen_lsn", "database_lsn":
				normalized[key] = "<lsn>"
			case "txids":
				normalized[key] = []any{"<txid>"}
			case "tags":
				continue
			case "xmin", "xmax":
				normalized[key] = "<xid>"
			case "xip_list":
				normalized[key] = normalizeXIPList(inner)
			case "snapshot_mark":
				normalized[key] = "<snapshot_mark>"
			case "offset":
				normalized[key] = normalizeOffset(fmt.Sprint(inner))
			default:
				normalized[key] = normalizeDynamic(inner)
			}
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, normalizeDynamic(item))
		}
		return normalized
	default:
		return value
	}
}

func normalizeXIPList(value any) any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	if len(items) == 0 {
		return []any{}
	}
	return []any{"<xid>"}
}

func normalizeOffset(value string) string {
	parts := strings.SplitN(value, "_", 2)
	if len(parts) != 2 {
		return value
	}
	if parts[0] == "0" {
		return value
	}
	return "<lsn>_" + parts[1]
}

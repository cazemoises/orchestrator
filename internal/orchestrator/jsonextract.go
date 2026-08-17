package orchestrator

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var codeFenceRE = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

// extractJSON pulls a single JSON object out of raw model output, tolerating
// the model wrapping its answer in a markdown code fence or adding
// prose before/after the JSON, even when explicitly instructed not to.
func extractJSON(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)

	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}

	if m := codeFenceRE.FindStringSubmatch(trimmed); m != nil {
		inner := strings.TrimSpace(m[1])
		if json.Valid([]byte(inner)) {
			return inner, nil
		}
	}

	if block, ok := firstBraceBlock(trimmed); ok {
		return block, nil
	}

	return "", errors.New("orchestrator: no valid JSON object found in model output")
}

// firstBraceBlock scans s for the first top-level {...} block, honoring
// string literals (so a '}' inside a JSON string value does not end the
// block early) and returns it if it is valid JSON.
func firstBraceBlock(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if json.Valid([]byte(candidate)) {
					return candidate, true
				}
				return "", false
			}
		}
	}

	return "", false
}

package orchestrator

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var codeFenceRE = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

// unquotedKeyRE matches a bare identifier in JSON object-key position: right
// after a '{' or ',' (allowing whitespace), followed by a colon.
var unquotedKeyRE = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*:)`)

// quoteUnquotedKeys is a best-effort normalizer for JS-object-literal-style
// output from the model (e.g. {ok: true} instead of {"ok": true}), which
// shows up occasionally even when the model is explicitly told to return
// JSON. It only quotes bare identifiers immediately after '{' or ',' -
// never touches values - and is intentionally not a full JS parser: a
// string VALUE that itself contains something like ", note:" can still
// fool it. That tradeoff is fine here because it is only ever applied
// after a straight json.Valid check on the same candidate has already
// failed (see validOrNormalized), so it never touches JSON that parsed
// correctly on its own.
func quoteUnquotedKeys(s string) string {
	return unquotedKeyRE.ReplaceAllString(s, `$1"$2"$3`)
}

// validOrNormalized returns candidate unchanged if it is already valid
// JSON. Otherwise it tries quoteUnquotedKeys as a fallback and returns that
// if IT then parses. The straight validity check always runs first, so
// already-valid JSON is never touched by normalization.
func validOrNormalized(candidate string) (string, bool) {
	if json.Valid([]byte(candidate)) {
		return candidate, true
	}
	normalized := quoteUnquotedKeys(candidate)
	if json.Valid([]byte(normalized)) {
		return normalized, true
	}
	return "", false
}

// extractJSON pulls a single JSON object out of raw model output, tolerating
// the model wrapping its answer in a markdown code fence, adding prose
// before/after the JSON (even when explicitly instructed not to), or using
// JS-object-literal syntax with unquoted keys instead of JSON.
func extractJSON(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)

	if normalized, ok := validOrNormalized(trimmed); ok {
		return normalized, nil
	}

	if m := codeFenceRE.FindStringSubmatch(trimmed); m != nil {
		inner := strings.TrimSpace(m[1])
		if normalized, ok := validOrNormalized(inner); ok {
			return normalized, nil
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
				return validOrNormalized(candidate)
			}
		}
	}

	return "", false
}

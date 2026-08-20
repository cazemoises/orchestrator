package claudecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// streamTextTruncateLen caps how many runes of a text/thinking block get
// logged per event, so a long chain of reasoning doesn't flood the
// terminal.
const streamTextTruncateLen = 200

// streamContentBlock is one entry of an assistant message's "content" array
// in a `--output-format stream-json` NDJSON event. Only the fields the
// summarizer cares about are declared; everything else (ids, signatures,
// caller info, etc) is ignored by encoding/json.
type streamContentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	Thinking string         `json:"thinking"`
	Name     string         `json:"name"`
	Input    map[string]any `json:"input"`
}

// streamEvent is one NDJSON line from `--output-format stream-json`. Only
// "assistant" events (text/thinking/tool_use content blocks) are summarized;
// "system", "user" (tool_result) and "rate_limit_event" lines are ignored -
// they're either internal bookkeeping or already implied by the tool_use
// that preceded them.
type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []streamContentBlock `json:"content"`
	} `json:"message"`
}

// summarizeStreamEvent parses one NDJSON line and, if it's an assistant
// event with something worth surfacing, returns a short human-readable
// summary (possibly multiple lines, one per content block) and ok=true.
// Anything else - malformed JSON, blank lines, non-assistant events, empty
// text/thinking blocks - returns ok=false.
func summarizeStreamEvent(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return "", false
	}
	if ev.Type != "assistant" {
		return "", false
	}

	var summaries []string
	for _, block := range ev.Message.Content {
		switch block.Type {
		case "tool_use":
			summaries = append(summaries, summarizeToolUse(block))
		case "text":
			if t := strings.TrimSpace(block.Text); t != "" {
				summaries = append(summaries, "[Dev] "+truncateRunes(t, streamTextTruncateLen))
			}
		case "thinking":
			if t := strings.TrimSpace(block.Thinking); t != "" {
				summaries = append(summaries, "[Dev] "+truncateRunes(t, streamTextTruncateLen))
			}
		}
	}
	if len(summaries) == 0 {
		return "", false
	}
	return strings.Join(summaries, "\n"), true
}

func summarizeToolUse(block streamContentBlock) string {
	switch block.Name {
	case "Bash":
		if cmd, ok := block.Input["command"].(string); ok && cmd != "" {
			return "[Dev] executando: " + cmd
		}
	case "Write", "Edit":
		if path, ok := block.Input["file_path"].(string); ok && path != "" {
			return fmt.Sprintf("[Dev] usando ferramenta %s em %s", block.Name, filepath.Base(path))
		}
	}
	return "[Dev] usando ferramenta " + block.Name
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// streamEventLogger receives one summarized event at a time, in the order
// they arrive - typically log.Printf, wrapped so scanDevStream doesn't need
// to depend on the log package directly.
type streamEventLogger func(summary string)

// scanDevStream reads NDJSON lines from r as they arrive (bufio.Scanner, not
// buffered whole), calling onEvent with a short summary for each one worth
// surfacing in real time (see summarizeStreamEvent). It returns:
//
//   - fullOutput: every "assistant"/"user"/"result" line received,
//     newline-joined exactly as read - kept for rate-limit substring
//     detection and debugging, mirroring what the non-streaming path already
//     captures in a bytes.Buffer. "system" and "rate_limit_event" lines are
//     deliberately excluded: they're internal protocol bookkeeping, not
//     content, and their own type names contain substrings
//     (RateLimitIndicators' "rate_limit", for one - rate_limit_event fires
//     on essentially every real call, informationally, regardless of actual
//     rate-limit status) that would otherwise false-trigger isRateLimited on
//     every streamed Dev call.
//   - resultLine: the raw JSON of the terminal "result" event, if one
//     arrived. It has the same field names as `--output-format json`'s
//     single envelope (result/is_error/num_turns/duration_ms), so the caller
//     feeds it to parseEnvelope exactly like the non-streaming stdout.
func scanDevStream(r io.Reader, onEvent streamEventLogger) (fullOutput, resultLine string, err error) {
	scanner := bufio.NewScanner(r)
	// Assistant text/thinking blocks can be large; grow well past
	// bufio.Scanner's 64KB default so a single long line never truncates or
	// errors out the whole stream.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var out strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		t := lineType(line)

		if t != "system" && t != "rate_limit_event" {
			out.WriteString(line)
			out.WriteByte('\n')
		}
		if summary, ok := summarizeStreamEvent(line); ok && onEvent != nil {
			onEvent(summary)
		}
		if t == "result" {
			resultLine = line
		}
	}
	return out.String(), resultLine, scanner.Err()
}

// lineType returns a stream-json line's top-level "type" field, or "" if the
// line isn't valid JSON (blank lines, partial writes, etc).
func lineType(line string) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &probe); err != nil {
		return ""
	}
	return probe.Type
}

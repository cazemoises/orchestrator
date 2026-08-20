package claudecli

import (
	"strings"
	"testing"
)

// sampleStream is a realistic (trimmed) NDJSON transcript as produced by
// `claude -p --output-format stream-json --verbose`, captured by hand
// against the real CLI: system/hook events, an assistant "thinking" block, a
// Bash tool_use + its tool_result, a Write tool_use + its tool_result, a
// final assistant text block, and the terminal "result" event (which has
// the same field names as `--output-format json`'s single envelope).
const sampleStream = `{"type":"system","subtype":"hook_started","hook_id":"h1"}
{"type":"system","subtype":"init","cwd":"/repo","model":"claude-sonnet-5"}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"escolhendo a abordagem mais simples antes de implementar"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./...","description":"run tests"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":false}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"/repo/internal/domain/book.go","content":"package domain\n"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"File created","is_error":false}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Pronto, implementei o adapter e os testes passam."}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":4,"duration_ms":5231.0,"result":"Pronto, implementei o adapter e os testes passam."}
`

func TestScanDevStream_SummarizesEventsAsTheyArrive(t *testing.T) {
	var got []string
	fullOutput, resultLine, err := scanDevStream(strings.NewReader(sampleStream), func(summary string) {
		got = append(got, summary)
	})
	if err != nil {
		t.Fatalf("scanDevStream returned error: %v", err)
	}

	want := []string{
		"[Dev] escolhendo a abordagem mais simples antes de implementar",
		"[Dev] executando: go test ./...",
		"[Dev] usando ferramenta Write em book.go",
		"[Dev] Pronto, implementei o adapter e os testes passam.",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d summaries, want %d\ngot: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("summary[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// "system" lines are deliberately excluded from fullOutput (see
	// TestScanDevStream_ExcludesSystemAndRateLimitEventLines), but
	// assistant/user/result content must all still be there.
	if strings.Contains(fullOutput, `"type":"system"`) {
		t.Fatalf("fullOutput should not contain raw system lines, got: %q", fullOutput)
	}
	for _, want := range []string{`"type":"assistant"`, `"type":"user"`, `"type":"result"`} {
		if !strings.Contains(fullOutput, want) {
			t.Fatalf("fullOutput missing %s lines, got: %q", want, fullOutput)
		}
	}

	env, err := parseEnvelope(resultLine)
	if err != nil {
		t.Fatalf("parseEnvelope(resultLine) returned error: %v", err)
	}
	if env.Result != "Pronto, implementei o adapter e os testes passam." || env.IsError || env.NumTurns != 4 || env.DurationMS != 5231.0 {
		t.Fatalf("got envelope %+v from resultLine, want it to match the terminal result event", env)
	}
}

// TestScanDevStream_ExcludesSystemAndRateLimitEventLines reproduces a real
// false-positive found by running the real `claude -p --output-format
// stream-json` against a working, non-rate-limited call: it emits a
// `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed",...}}`
// line on essentially every call, purely informational. Before this fix,
// that line's own type name (containing the substring "rate_limit", one of
// RateLimitIndicators) ended up in fullOutput and made isRateLimited(fullOutput)
// return true for a call that succeeded normally - every single streamed Dev
// call would have been misidentified as rate-limited.
func TestScanDevStream_ExcludesSystemAndRateLimitEventLines(t *testing.T) {
	stream := `{"type":"system","subtype":"init","cwd":"/repo"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1787257800}}
{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":1,"duration_ms":100,"result":"done"}
`
	fullOutput, resultLine, err := scanDevStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("scanDevStream returned error: %v", err)
	}
	if strings.Contains(fullOutput, "rate_limit_event") || strings.Contains(fullOutput, "rate_limit_info") {
		t.Fatalf("fullOutput must not contain the informational rate_limit_event line, got: %q", fullOutput)
	}
	if strings.Contains(fullOutput, `"type":"system"`) {
		t.Fatalf("fullOutput must not contain raw system lines, got: %q", fullOutput)
	}
	if isRateLimited(fullOutput) {
		t.Fatalf("isRateLimited(fullOutput) = true for a normal successful stream, want false")
	}
	if resultLine == "" {
		t.Fatal("expected resultLine to still be captured despite the excluded lines")
	}
}

func TestScanDevStream_IgnoresSystemAndToolResultLines(t *testing.T) {
	var got []string
	if _, _, err := scanDevStream(strings.NewReader(sampleStream), func(summary string) {
		got = append(got, summary)
	}); err != nil {
		t.Fatalf("scanDevStream returned error: %v", err)
	}
	for _, s := range got {
		if strings.Contains(s, "hook_started") || strings.Contains(s, "tool_result") {
			t.Fatalf("summary %q should not surface raw system/tool_result events", s)
		}
	}
}

func TestSummarizeStreamEvent_TruncatesLongText(t *testing.T) {
	longText := strings.Repeat("a", 500)
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + longText + `"}]}}`

	summary, ok := summarizeStreamEvent(line)
	if !ok {
		t.Fatal("expected summarizeStreamEvent to return ok=true for a text block")
	}
	if len(summary) >= len(longText) {
		t.Fatalf("got summary of length %d, want it truncated well below the original %d chars", len(summary), len(longText))
	}
	if !strings.HasSuffix(summary, "...") {
		t.Fatalf("got summary %q, want it to end with a truncation marker", summary)
	}
}

func TestSummarizeStreamEvent_IgnoresNonAssistantLines(t *testing.T) {
	for _, line := range []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"result","subtype":"success","result":"done"}`,
		``,
		`not json at all`,
	} {
		if _, ok := summarizeStreamEvent(line); ok {
			t.Errorf("summarizeStreamEvent(%q) returned ok=true, want false", line)
		}
	}
}

func TestSummarizeStreamEvent_BashToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go vet ./..."}}]}}`
	got, ok := summarizeStreamEvent(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "[Dev] executando: go vet ./..." {
		t.Fatalf("got %q, want %q", got, "[Dev] executando: go vet ./...")
	}
}

func TestSummarizeStreamEvent_UnknownToolUseFallsBackToToolName(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}}]}}`
	got, ok := summarizeStreamEvent(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "[Dev] usando ferramenta Glob" {
		t.Fatalf("got %q, want %q", got, "[Dev] usando ferramenta Glob")
	}
}

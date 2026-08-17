package orchestrator

import "testing"

func TestExtractJSON_PlainJSONPassesThrough(t *testing.T) {
	got, err := extractJSON(`{"action":"next_task","task_id":"t1"}`)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"action":"next_task","task_id":"t1"}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_StripsWhitespace(t *testing.T) {
	got, err := extractJSON("\n\n  {\"a\":1}  \n")
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_StripsJSONCodeFence(t *testing.T) {
	raw := "```json\n{\"action\":\"product_done\"}\n```"
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"action":"product_done"}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_StripsPlainCodeFence(t *testing.T) {
	raw := "```\n{\"verdict\":\"accept\"}\n```"
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"verdict":"accept"}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_ExtractsFirstBraceBlockWithSurroundingText(t *testing.T) {
	raw := "Sure, here is my decision:\n{\"verdict\":\"reject\",\"reasoning\":\"tests fail\"}\nLet me know if you need anything else."
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"verdict":"reject","reasoning":"tests fail"}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_HandlesNestedBraces(t *testing.T) {
	raw := "prefix {\"a\":{\"b\":1},\"c\":2} suffix"
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"a":{"b":1},"c":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_HandlesBracesInsideStringValues(t *testing.T) {
	raw := `prefix {"reasoning":"the map has a { in it"} suffix`
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"reasoning":"the map has a { in it"}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSON_ReturnsErrorWhenNoJSONPresent(t *testing.T) {
	if _, err := extractJSON("I could not find anything to do."); err == nil {
		t.Fatal("expected error when no JSON object is present")
	}
}

// --- loose/unquoted-key normalization ---------------------------------

func TestExtractJSON_QuotesSimpleUnquotedKey(t *testing.T) {
	got, err := extractJSON(`{ok: true}`)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"ok": true}` {
		t.Fatalf("got %q, want %q", got, `{"ok": true}`)
	}
}

func TestExtractJSON_QuotesUnquotedKeysInNestedObjects(t *testing.T) {
	got, err := extractJSON(`{outer: {inner: 1}}`)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"outer": {"inner": 1}}` {
		t.Fatalf("got %q, want %q", got, `{"outer": {"inner": 1}}`)
	}
}

func TestExtractJSON_QuotesOnlyTheUnquotedKeysInMixedObject(t *testing.T) {
	got, err := extractJSON(`{"a": 1, b: 2}`)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != `{"a": 1, "b": 2}` {
		t.Fatalf("got %q, want %q", got, `{"a": 1, "b": 2}`)
	}
}

func TestExtractJSON_AlreadyValidJSONIsReturnedUnmodified(t *testing.T) {
	raw := `{"a": 1, "b": 2}`
	got, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("extractJSON returned error: %v", err)
	}
	if got != raw {
		t.Fatalf("got %q, want the input returned byte-for-byte unmodified: %q", got, raw)
	}
}

func TestExtractJSON_LoosePODecisionParsesAfterNormalization(t *testing.T) {
	raw := `{action: "next_task", task_id: "t1", dev_prompt: "do it", reasoning: "because"}`
	decision, err := parsePODecision(raw)
	if err != nil {
		t.Fatalf("parsePODecision returned error: %v", err)
	}
	if decision.Action != "next_task" || decision.TaskID != "t1" || decision.DevPrompt != "do it" || decision.Reasoning != "because" {
		t.Fatalf("got %+v, want fully parsed decision", decision)
	}
}

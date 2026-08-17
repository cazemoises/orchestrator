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

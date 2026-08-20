package orchestrator

import (
	"strings"
	"testing"
)

func TestPODecidePrompt_IncludesAllInputs(t *testing.T) {
	got := poDecidePrompt("VISION-MARKER", "STATE-MARKER", `[{"id":"t1"}]`)

	for _, want := range []string{"VISION-MARKER", "STATE-MARKER", `[{"id":"t1"}]`} {
		if !contains(got, want) {
			t.Errorf("prompt missing input %q\n---\n%s", want, got)
		}
	}
}

func TestPODecidePrompt_InstructsJSONOnlyResponseFormat(t *testing.T) {
	got := poDecidePrompt("v", "s", "[]")
	for _, want := range []string{"action", "task_id", "title", "description", "dev_prompt", "reasoning", "next_task", "product_done"} {
		if !contains(got, want) {
			t.Errorf("prompt missing PODecision field %q", want)
		}
	}
}

func TestPODecidePrompt_InstructsTitleAndDescriptionRequiredEvenForInventedTaskIDs(t *testing.T) {
	got := poDecidePrompt("v", "s", "[]")
	for _, want := range []string{"inventar", "esgotou"} {
		if !contains(got, want) {
			t.Errorf("prompt missing instruction covering %q (title/description required even for a task_id invented outside the backlog)", want)
		}
	}
}

func TestPODecidePrompt_InstructsSelfContainedArchitecturalConventions(t *testing.T) {
	got := poDecidePrompt("v", "s", "[]")
	for _, want := range []string{"hexagonal", "TDD", "RED", "GREEN", "commit"} {
		if !contains(got, want) {
			t.Errorf("prompt missing architectural convention reminder %q", want)
		}
	}
}

func TestPOEvaluatePrompt_IncludesAllInputs(t *testing.T) {
	got := poEvaluatePrompt("VISION-MARKER", "STATE-MARKER", `{"task_id":"t1"}`)

	for _, want := range []string{"VISION-MARKER", "STATE-MARKER", `{"task_id":"t1"}`} {
		if !contains(got, want) {
			t.Errorf("prompt missing input %q\n---\n%s", want, got)
		}
	}
}

func TestPOEvaluatePrompt_InstructsJSONOnlyResponseFormat(t *testing.T) {
	got := poEvaluatePrompt("v", "s", "{}")
	for _, want := range []string{"verdict", "accept", "reject", "block", "correction_prompt", "state_update"} {
		if !contains(got, want) {
			t.Errorf("prompt missing POEvaluation field %q", want)
		}
	}
}

func TestPOEvaluatePrompt_InstructsRigorousVerification(t *testing.T) {
	got := poEvaluatePrompt("v", "s", "{}")
	for _, want := range []string{"teste", "arquivo"} {
		if !contains(got, want) {
			t.Errorf("prompt missing rigor instruction covering %q", want)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

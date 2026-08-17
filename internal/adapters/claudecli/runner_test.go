package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"orchestrator/internal/ports"
)

// --- isRateLimited -----------------------------------------------------

func TestIsRateLimited_DetectsKnownSubstringsCaseInsensitively(t *testing.T) {
	cases := []string{
		"You have hit your USAGE LIMIT for this period.",
		"error: rate limit exceeded",
		"rate_limit_error: please slow down",
		"Quota Exceeded for this account",
		"Please try again later.",
		"5-hour limit reached, resets at 3pm",
	}
	for _, c := range cases {
		if !isRateLimited(c) {
			t.Errorf("isRateLimited(%q) = false, want true", c)
		}
	}
}

func TestIsRateLimited_ReturnsFalseForOrdinaryOutput(t *testing.T) {
	if isRateLimited("here is the result of your task, all good") {
		t.Fatal("expected ordinary output not to be flagged as rate limited")
	}
}

// --- buildArgs -----------------------------------------------------------

func TestBuildArgs_IncludesPromptAndOutputFormat(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "do the thing"}, true)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--output-format json") {
		t.Fatalf("args %v missing --output-format json", args)
	}
	if args[len(args)-1] != "do the thing" {
		t.Fatalf("args %v: expected prompt as last argument", args)
	}
}

func TestBuildArgs_IncludesAllowedToolsWhenSet(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", AllowedTools: []string{"Bash", "Edit"}}, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--allowedTools") || !strings.Contains(joined, "Bash,Edit") {
		t.Fatalf("args %v missing --allowedTools Bash,Edit", args)
	}
}

func TestBuildArgs_IncludesModelWhenSet(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", Model: "sonnet"}, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model sonnet") {
		t.Fatalf("args %v missing --model sonnet", args)
	}
}

func TestBuildArgs_OmitsMaxTurnsWhenUnsupported(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", MaxTurns: 30}, false)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "max-turns") {
		t.Fatalf("args %v should not contain max-turns when unsupported", args)
	}
}

func TestBuildArgs_IncludesMaxTurnsWhenSupportedAndSet(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", MaxTurns: 30}, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--max-turns 30") {
		t.Fatalf("args %v missing --max-turns 30", args)
	}
}

func TestBuildArgs_OmitsMaxTurnsWhenZero(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", MaxTurns: 0}, true)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "max-turns") {
		t.Fatalf("args %v should not contain max-turns when MaxTurns is 0", args)
	}
}

// --- parseEnvelope ---------------------------------------------------------

func TestParseEnvelope_ParsesSuccessfulResult(t *testing.T) {
	raw := `{"result": "all done", "is_error": false, "num_turns": 4, "duration_ms": 2500}`
	env, err := parseEnvelope(raw)
	if err != nil {
		t.Fatalf("parseEnvelope returned error: %v", err)
	}
	if env.Result != "all done" || env.IsError || env.NumTurns != 4 || env.DurationMS != 2500 {
		t.Fatalf("got %+v, want parsed fields", env)
	}
}

func TestParseEnvelope_ReturnsErrorOnInvalidJSON(t *testing.T) {
	if _, err := parseEnvelope("not json at all"); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// --- Runner.Run (full orchestration, exec faked) ---------------------------

type fakeExec struct {
	stdout, stderr string
	err            error
	gotArgs        []string
}

func (f *fakeExec) run(ctx context.Context, workDir string, args []string) (string, string, error) {
	f.gotArgs = args
	return f.stdout, f.stderr, f.err
}

func TestRun_ParsesSuccessfulEnvelope(t *testing.T) {
	fe := &fakeExec{stdout: `{"result": "42", "is_error": false, "num_turns": 2, "duration_ms": 1000}`}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "what is 6*7"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success || res.Output != "42" || res.NumTurns != 2 || res.DurationSec != 1 {
		t.Fatalf("got %+v, want successful parsed result", res)
	}
}

func TestRun_MarksIsErrorAsUnsuccessful(t *testing.T) {
	fe := &fakeExec{stdout: `{"result": "couldn't finish", "is_error": true, "num_turns": 1}`}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Success {
		t.Fatalf("got Success=true, want false for is_error envelope")
	}
}

func TestRun_DetectsRateLimitFromStderrBeforeParsing(t *testing.T) {
	fe := &fakeExec{stdout: "", stderr: "Error: usage limit reached for this account"}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.RateLimited {
		t.Fatalf("got RateLimited=false, want true")
	}
}

func TestRun_ReturnsFailedResultOnUnparsableOutput(t *testing.T) {
	fe := &fakeExec{stdout: "totally not json"}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run should not return a Go error on unparsable output, got: %v", err)
	}
	if res.Success {
		t.Fatalf("got Success=true, want false for unparsable output")
	}
	if res.ErrorMsg == "" {
		t.Fatalf("expected ErrorMsg to explain the parse failure")
	}
}

func TestRun_ReturnsErrorWhenExecFailsToStart(t *testing.T) {
	wantErr := errors.New("exec: \"claude\": executable file not found in $PATH")
	fe := &fakeExec{err: wantErr}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	_, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected Run to propagate exec start failure as error")
	}
}

func TestRun_ProbesMaxTurnsSupportOnceWhenUnknown(t *testing.T) {
	fe := &fakeExec{stdout: `{"result": "ok", "is_error": false, "num_turns": 1}`}
	probeCalls := 0
	r := &Runner{
		exec: fe.run,
		helpFunc: func(ctx context.Context) (string, error) {
			probeCalls++
			return "--allowedTools\n--max-turns <n>\n--model", nil
		},
	}

	if _, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x", MaxTurns: 10}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(strings.Join(fe.gotArgs, " "), "--max-turns 10") {
		t.Fatalf("gotArgs %v: expected --max-turns 10 after successful probe", fe.gotArgs)
	}
	if probeCalls != 1 {
		t.Fatalf("got %d probe calls, want 1", probeCalls)
	}

	if _, err := r.Run(context.Background(), ports.RunRequest{Prompt: "y", MaxTurns: 10}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("got %d probe calls on second Run, want cached result (still 1)", probeCalls)
	}
}

func boolPtr(b bool) *bool { return &b }

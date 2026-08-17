package claudecli

import (
	"context"
	"errors"
	"fmt"
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

func TestIsRateLimited_DetectsRealSessionLimitMessage(t *testing.T) {
	msg := "You've hit your session limit · resets 2:30am (America/Sao_Paulo)"
	if !isRateLimited(msg) {
		t.Fatalf("isRateLimited(%q) = false, want true", msg)
	}
}

// --- buildArgs -----------------------------------------------------------

func TestBuildArgs_IncludesOutputFormat(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "do the thing"}, true)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--output-format json") {
		t.Fatalf("args %v missing --output-format json", args)
	}
}

func TestBuildArgs_DoesNotIncludePromptAsArgument(t *testing.T) {
	// The prompt goes over stdin, never as a CLI argument: on Windows,
	// cmd.exe truncates command lines around ~8191 characters, which
	// silently mangles large prompts (vision.md + backlog.json embedded)
	// before they ever reach the claude process.
	args := buildArgs(ports.RunRequest{Prompt: "do the thing"}, true)

	for _, a := range args {
		if strings.Contains(a, "do the thing") {
			t.Fatalf("args %v: prompt must not appear as a CLI argument", args)
		}
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

func TestBuildArgs_IncludesSkipPermissionsFlagWhenRequested(t *testing.T) {
	args := buildArgs(ports.RunRequest{Prompt: "x", SkipPermissions: true}, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("args %v missing --dangerously-skip-permissions", args)
	}
}

func TestBuildArgs_OmitsSkipPermissionsFlagByDefault(t *testing.T) {
	// PO calls never set SkipPermissions - the PO doesn't touch files, so
	// it keeps Claude Code's normal permission prompts as a safety net.
	args := buildArgs(ports.RunRequest{Prompt: "x"}, true)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "skip-permissions") {
		t.Fatalf("args %v should not contain the skip-permissions flag when SkipPermissions is false", args)
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
	gotStdin       string
}

func (f *fakeExec) run(ctx context.Context, workDir string, args []string, stdin string) (string, string, error) {
	f.gotArgs = args
	f.gotStdin = stdin
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

// TestRun_DetectsRealSessionLimitMessageAsRateLimited reproduces the actual
// bug: the CLI's current session-limit wording wasn't in RateLimitIndicators,
// so it fell through to parseEnvelope, failed as invalid JSON, and the
// orchestrator treated the rate limit as a normal parse failure instead of
// entering the wait/retry path.
func TestRun_DetectsRealSessionLimitMessageAsRateLimited(t *testing.T) {
	fe := &fakeExec{stdout: "You've hit your session limit · resets 2:30am (America/Sao_Paulo)"}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.RateLimited {
		t.Fatalf("got RateLimited=false, want true for real session limit message")
	}
}

// TestRun_UnrecognizedNonJSONOutputFallsBackToRateLimited exercises the
// defense-in-depth layer: a future CLI wording change that matches none of
// the known markers must still be treated as a possible rate limit, not
// silently misparsed as a normal decision, as long as the output contains
// no '{' at all (i.e. it is clearly not even an attempt at JSON).
func TestRun_UnrecognizedNonJSONOutputFallsBackToRateLimited(t *testing.T) {
	fe := &fakeExec{stdout: "Sorry, you'll need to wait a bit before continuing."}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.RateLimited {
		t.Fatalf("got RateLimited=false, want true for unrecognized non-JSON output (fallback heuristic)")
	}
}

// TestRun_ValidSuccessfulJSONNotAffectedByFallbackHeuristic guards against
// the fallback heuristic above from misfiring on normal, well-formed
// successful envelopes: Success=true and the output does contain '{', so
// neither leg of the fallback OR condition should fire.
func TestRun_ValidSuccessfulJSONNotAffectedByFallbackHeuristic(t *testing.T) {
	fe := &fakeExec{stdout: `{"result": "42", "is_error": false, "num_turns": 2, "duration_ms": 1000}`}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	res, err := r.Run(context.Background(), ports.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.RateLimited {
		t.Fatalf("got RateLimited=true, want false for a normal valid JSON success envelope")
	}
	if !res.Success || res.Output != "42" {
		t.Fatalf("got %+v, want unaffected successful parsed result", res)
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

func TestRun_SendsPromptViaStdinNotArgs(t *testing.T) {
	fe := &fakeExec{stdout: `{"result": "ok", "is_error": false, "num_turns": 1}`}
	r := &Runner{exec: fe.run, maxTurnsSupported: boolPtr(true)}

	prompt := "this is the prompt body, it must go over stdin"
	if _, err := r.Run(context.Background(), ports.RunRequest{Prompt: prompt}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if fe.gotStdin != prompt {
		t.Fatalf("got stdin %q, want prompt %q", fe.gotStdin, prompt)
	}
	for _, a := range fe.gotArgs {
		if strings.Contains(a, prompt) {
			t.Fatalf("gotArgs %v: prompt leaked into CLI arguments", fe.gotArgs)
		}
	}
}

// TestRunProcess_LargeStdinArrivesIntact exercises the real os/exec plumbing
// (not the fakeExec seam) with a prompt far larger than Windows cmd.exe's
// ~8191-character command-line limit, using `cat` (portable, no network
// dependency) as a stand-in for `claude` to prove stdin - not argv - is how
// the payload travels, and that it survives the trip byte-for-byte.
func TestRunProcess_LargeStdinArrivesIntact(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Vision\n\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, `{"id":"task-%d","title":"do the thing number %d","status":"pending"}`, i, i)
	}
	largePrompt := b.String()
	if len(largePrompt) <= 10000 {
		t.Fatalf("test setup bug: largePrompt is only %d chars, want >10000", len(largePrompt))
	}

	stdout, stderr, err := runProcess(context.Background(), "cat", "", nil, largePrompt)
	if err != nil {
		t.Fatalf("runProcess returned error: %v (stderr: %s)", err, stderr)
	}
	if stdout != largePrompt {
		t.Fatalf("got stdout of length %d, want %d to match input exactly", len(stdout), len(largePrompt))
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

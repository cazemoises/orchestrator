// Package claudecli implements ports.AgentRunner by shelling out to the
// Claude Code CLI (`claude -p ...`), authenticated via
// CLAUDE_CODE_OAUTH_TOKEN (a Pro/Max subscription login, not an API key).
package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"orchestrator/internal/ports"
)

var _ ports.AgentRunner = (*Runner)(nil)

// RateLimitIndicators are lowercase substrings that, if found anywhere in
// the combined stdout+stderr of a `claude -p` invocation, mark the call as
// rate-limited rather than failed.
//
// IMPORTANT: this is a best-effort heuristic based on the wording Claude
// Code used at the time this was written. The exact message text is not a
// stable API and can change between CLI versions. After the orchestrator's
// first real rate limit on the VM, check the raw output that was logged
// and update this list by hand if it no longer matches.
var RateLimitIndicators = []string{
	"usage limit",
	"rate limit",
	"rate_limit",
	"quota exceeded",
	"try again later",
	"limit reached",
	"session limit",
	"hit your",
}

func isRateLimited(combinedOutput string) bool {
	lower := strings.ToLower(combinedOutput)
	for _, indicator := range RateLimitIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// resetTimeRegex matches the reset time embedded in Claude Code's rate
// limit message, e.g. "You've hit your session limit · resets 2:30am
// (America/Sao_Paulo)" - capturing the hour, minute, am/pm, and IANA
// timezone name.
var resetTimeRegex = regexp.MustCompile(`(?i)resets (\d{1,2}):(\d{2})\s*(am|pm) \(([^)]+)\)`)

// parseResetTime extracts the reset time embedded in a rate-limit message
// (see resetTimeRegex) and resolves it to a concrete time.Time in the
// message's own timezone, relative to now. Resets are daily: if the clock
// time named in the message has already passed today, it must mean
// tomorrow's reset, not today's already-elapsed one.
//
// Returns nil if the message doesn't contain a recognizable reset time
// (unknown wording, unparseable timezone, etc) - callers must fall back to
// a generic backoff schedule in that case (see orchestrator/loop.go's
// resolveRateLimitWait). This is what lets the orchestrator wait until the
// CLI's own stated reset time instead of a blind exponential backoff that
// once left it waiting almost 12h with no idea when it would actually
// resume.
func parseResetTime(message string, now time.Time) *time.Time {
	m := resetTimeRegex.FindStringSubmatch(message)
	if m == nil {
		return nil
	}

	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return nil
	}
	minute, err := strconv.Atoi(m[2])
	if err != nil || minute < 0 || minute > 59 {
		return nil
	}
	loc, err := time.LoadLocation(m[4])
	if err != nil {
		return nil
	}

	hour24 := hour % 12
	if strings.ToLower(m[3]) == "pm" {
		hour24 += 12
	}

	nowInLoc := now.In(loc)
	resetAt := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), hour24, minute, 0, 0, loc)
	if !resetAt.After(nowInLoc) {
		resetAt = resetAt.AddDate(0, 0, 1)
	}
	return &resetAt
}

// envelope is the subset of `claude -p --output-format json`'s output that
// the orchestrator cares about.
type envelope struct {
	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	DurationMS float64 `json:"duration_ms"`
}

func parseEnvelope(stdout string) (envelope, error) {
	var env envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		return envelope{}, fmt.Errorf("claudecli: parsing output as JSON: %w", err)
	}
	return env, nil
}

// buildArgs turns a RunRequest into `claude` CLI arguments. The prompt is
// deliberately NOT included here: it is sent over stdin instead (see
// execFunc), never as a CLI argument. On Windows, cmd.exe truncates command
// lines around ~8191 characters, which silently mangled large prompts
// (vision.md + backlog.json embedded in the PO prompt routinely exceed
// that) before they ever reached the claude process.
// supportsMaxTurns controls whether --max-turns is emitted at all: older
// and newer CLI builds have both shipped without it (see maxTurnsSupported
// probing below), and passing an unknown flag is a hard failure for the
// whole invocation.
func buildArgs(req ports.RunRequest, supportsMaxTurns bool) []string {
	// stream-json requires --verbose when combined with --print (`claude`
	// refuses to start otherwise, as of CLI 2.1.x). Only Dev calls ever set
	// StreamOutput - see ports.RunRequest and loop.go's stepDev.
	var args []string
	if req.StreamOutput {
		args = []string{"-p", "--output-format", "stream-json", "--verbose"}
	} else {
		args = []string{"-p", "--output-format", "json"}
	}

	if len(req.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(req.AllowedTools, ","))
	}
	if req.MaxTurns > 0 && supportsMaxTurns {
		args = append(args, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.SkipPermissions {
		// Claude Code normally pauses to ask interactive approval before a
		// tool call; in headless mode (-p, no TTY) there is nobody to
		// answer that prompt, so the Dev call would just hang forever
		// without this flag. Only set for Dev requests (never PO, which
		// doesn't touch files) - this is deliberately accepted as safe
		// here because: (a) AllowedTools already restricts which tools the
		// Dev can use at all, (b) WorkDir is always a specific project
		// directory, never the whole machine, and (c) the orchestrator's
		// LocalVerifyCommand + GitPusher guardrail (see loop.go's
		// handleAccept) stop broken code from ever being committed or
		// pushed even if the Dev does something wrong under this flag.
		// See README.md, "Por que --dangerously-skip-permissions é seguro
		// neste contexto específico" for the full writeup.
		args = append(args, "--dangerously-skip-permissions")
	}

	return args
}

// execFunc runs a binary with the prompt fed over stdin and returns its
// stdout/stderr. It is a seam for testing: production code uses
// runViaOSExec; tests inject a fake.
type execFunc func(ctx context.Context, workDir string, args []string, stdin string) (stdout, stderr string, err error)

// runProcess runs binPath with args in workDir, writing stdin to the
// process's standard input. Split out from runViaOSExec so tests can
// exercise the real os/exec stdin plumbing against a portable stand-in
// binary (e.g. `cat`) instead of the real `claude`.
func runProcess(ctx context.Context, binPath, workDir string, args []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdin)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if _, isExitErr := err.(*exec.ExitError); isExitErr {
		// A non-zero exit from `claude -p` is a normal, expected outcome
		// (rate limit, agent failure, etc.) - it still produced output for
		// us to interpret, so it is not a Go-level error.
		err = nil
	}
	return stdout.String(), stderr.String(), err
}

func runViaOSExec(ctx context.Context, workDir string, args []string, stdin string) (string, string, error) {
	return runProcess(ctx, "claude", workDir, args, stdin)
}

// streamExecFunc is runProcessStreaming's signature - a seam for testing,
// mirroring execFunc but reading stdout line-by-line in real time via
// onEvent (see scanDevStream) instead of only returning once the process
// exits.
type streamExecFunc func(ctx context.Context, workDir string, args []string, stdin string, onEvent streamEventLogger) (stdout, stderr, resultLine string, err error)

// runProcessStreaming runs binPath with args in workDir, writing stdin to
// the process, and reads its stdout line-by-line as it's produced (unlike
// runProcess, which only sees output after the process exits) so onEvent
// can surface progress in real time. See scanDevStream for what fullOutput
// and resultLine mean.
func runProcessStreaming(ctx context.Context, binPath, workDir string, args []string, stdin string, onEvent streamEventLogger) (string, string, string, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdin)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", "", "", err
	}

	stdout, resultLine, scanErr := scanDevStream(stdoutPipe, onEvent)

	err = cmd.Wait()
	if _, isExitErr := err.(*exec.ExitError); isExitErr {
		// Same reasoning as runProcess: a non-zero exit is a normal outcome
		// here, not a Go-level error.
		err = nil
	}
	if err == nil && scanErr != nil {
		err = scanErr
	}
	return stdout, stderr.String(), resultLine, err
}

func runViaOSExecStreaming(ctx context.Context, workDir string, args []string, stdin string, onEvent streamEventLogger) (string, string, string, error) {
	return runProcessStreaming(ctx, "claude", workDir, args, stdin, onEvent)
}

// Runner is the production ports.AgentRunner backed by the claude CLI.
type Runner struct {
	exec              execFunc
	streamExec        streamExecFunc
	helpFunc          func(ctx context.Context) (string, error)
	maxTurnsSupported *bool

	// now is a seam for testing parseResetTime's today-vs-tomorrow
	// resolution deterministically; production code leaves it nil and
	// Run defaults it to time.Now.
	now func() time.Time
}

// NewRunner creates a Runner that shells out to the real `claude` binary.
func NewRunner() *Runner {
	return &Runner{exec: runViaOSExec, streamExec: runViaOSExecStreaming, now: time.Now}
}

func (r *Runner) probeMaxTurnsSupport(ctx context.Context) bool {
	if r.maxTurnsSupported != nil {
		return *r.maxTurnsSupported
	}

	helpFunc := r.helpFunc
	if helpFunc == nil {
		helpFunc = func(ctx context.Context) (string, error) {
			out, _, err := runViaOSExec(ctx, "", []string{"-p", "--help"}, "")
			return out, err
		}
	}

	help, err := helpFunc(ctx)
	supported := err == nil && strings.Contains(help, "--max-turns")
	r.maxTurnsSupported = &supported
	return supported
}

func (r *Runner) Run(ctx context.Context, req ports.RunRequest) (ports.RunResult, error) {
	supportsMaxTurns := false
	if req.MaxTurns > 0 {
		supportsMaxTurns = r.probeMaxTurnsSupport(ctx)
	}
	args := buildArgs(req, supportsMaxTurns)

	// envelopeSource is what gets handed to parseEnvelope: the whole stdout
	// in the non-streaming ("json") path, or just the terminal "result"
	// event's raw line in the streaming path (see scanDevStream) - stream
	// mode's stdout is many NDJSON lines, not the single JSON object
	// parseEnvelope expects.
	var stdout, stderr, envelopeSource string
	var err error

	if req.StreamOutput {
		streamExecFn := r.streamExec
		if streamExecFn == nil {
			streamExecFn = runViaOSExecStreaming
		}
		var resultLine string
		stdout, stderr, resultLine, err = streamExecFn(ctx, req.WorkDir, args, req.Prompt, func(summary string) {
			log.Printf("%s", summary)
		})
		envelopeSource = resultLine
	} else {
		execFn := r.exec
		if execFn == nil {
			execFn = runViaOSExec
		}
		stdout, stderr, err = execFn(ctx, req.WorkDir, args, req.Prompt)
		envelopeSource = stdout
	}
	if err != nil {
		return ports.RunResult{}, fmt.Errorf("claudecli: running claude: %w", err)
	}

	nowFn := r.now
	if nowFn == nil {
		nowFn = time.Now
	}

	combined := stdout + "\n" + stderr
	if isRateLimited(combined) {
		return ports.RunResult{
			RateLimited: true,
			Output:      stdout,
			ErrorMsg:    "rate limited",
			ResetAt:     parseResetTime(combined, nowFn()),
		}, nil
	}

	env, err := parseEnvelope(envelopeSource)
	if err != nil {
		// Defense in depth: RateLimitIndicators is a best-effort text match
		// and will always be fragile to CLI wording changes (see the
		// incident this guards against - the CLI's actual session-limit
		// message didn't match any known marker, so it fell through to
		// parseEnvelope, failed as invalid JSON, and got treated as a
		// normal blocked-task decision instead of entering the wait/retry
		// path). A response that fails to parse as the expected envelope,
		// or contains no '{' at all (clearly not even an attempt at JSON),
		// is treated as a possible rate limit out of caution, even though
		// no known marker matched.
		//
		// This must NOT extend to a response that parses successfully with
		// is_error:true: that is a genuine, well-formed execution/code
		// failure, not a rate limit - unlike a rate limit it will never
		// resolve itself by waiting, so it has to surface as a normal
		// Success=false failure and go through the existing reject/block
		// flow instead of waiting forever.
		log.Printf("claudecli: possível rate limit não reconhecido pelos markers conhecidos, tratando como tal por precaução: %s", combined)
		return ports.RunResult{
			RateLimited: true,
			Success:     false,
			Output:      stdout,
			ErrorMsg:    err.Error(),
			ResetAt:     parseResetTime(combined, nowFn()),
		}, nil
	}

	return ports.RunResult{
		Output:      env.Result,
		Success:     !env.IsError,
		DurationSec: env.DurationMS / 1000,
		NumTurns:    env.NumTurns,
	}, nil
}

// Package claudecli implements ports.AgentRunner by shelling out to the
// Claude Code CLI (`claude -p ...`), authenticated via
// CLAUDE_CODE_OAUTH_TOKEN (a Pro/Max subscription login, not an API key).
package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

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

// buildArgs turns a RunRequest into `claude` CLI arguments.
// supportsMaxTurns controls whether --max-turns is emitted at all: older
// and newer CLI builds have both shipped without it (see maxTurnsSupported
// probing below), and passing an unknown flag is a hard failure for the
// whole invocation.
func buildArgs(req ports.RunRequest, supportsMaxTurns bool) []string {
	args := []string{"-p", "--output-format", "json"}

	if len(req.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(req.AllowedTools, ","))
	}
	if req.MaxTurns > 0 && supportsMaxTurns {
		args = append(args, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	args = append(args, req.Prompt)
	return args
}

// execFunc runs the claude binary and returns its stdout/stderr. It is a
// seam for testing: production code uses runViaOSExec; tests inject a fake.
type execFunc func(ctx context.Context, workDir string, args []string) (stdout, stderr string, err error)

func runViaOSExec(ctx context.Context, workDir string, args []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir

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

// Runner is the production ports.AgentRunner backed by the claude CLI.
type Runner struct {
	exec              execFunc
	helpFunc          func(ctx context.Context) (string, error)
	maxTurnsSupported *bool
}

// NewRunner creates a Runner that shells out to the real `claude` binary.
func NewRunner() *Runner {
	return &Runner{exec: runViaOSExec}
}

func (r *Runner) probeMaxTurnsSupport(ctx context.Context) bool {
	if r.maxTurnsSupported != nil {
		return *r.maxTurnsSupported
	}

	helpFunc := r.helpFunc
	if helpFunc == nil {
		helpFunc = func(ctx context.Context) (string, error) {
			out, _, err := runViaOSExec(ctx, "", []string{"-p", "--help"})
			return out, err
		}
	}

	help, err := helpFunc(ctx)
	supported := err == nil && strings.Contains(help, "--max-turns")
	r.maxTurnsSupported = &supported
	return supported
}

func (r *Runner) Run(ctx context.Context, req ports.RunRequest) (ports.RunResult, error) {
	execFn := r.exec
	if execFn == nil {
		execFn = runViaOSExec
	}

	supportsMaxTurns := false
	if req.MaxTurns > 0 {
		supportsMaxTurns = r.probeMaxTurnsSupport(ctx)
	}

	args := buildArgs(req, supportsMaxTurns)
	stdout, stderr, err := execFn(ctx, req.WorkDir, args)
	if err != nil {
		return ports.RunResult{}, fmt.Errorf("claudecli: running claude: %w", err)
	}

	if isRateLimited(stdout + "\n" + stderr) {
		return ports.RunResult{
			RateLimited: true,
			Output:      stdout,
			ErrorMsg:    "rate limited",
		}, nil
	}

	env, err := parseEnvelope(stdout)
	if err != nil {
		return ports.RunResult{
			Success:  false,
			Output:   stdout,
			ErrorMsg: err.Error(),
		}, nil
	}

	return ports.RunResult{
		Output:      env.Result,
		Success:     !env.IsError,
		DurationSec: env.DurationMS / 1000,
		NumTurns:    env.NumTurns,
	}, nil
}

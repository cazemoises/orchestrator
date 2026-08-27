// Package ports declares the interfaces the orchestrator core depends on.
// Concrete implementations live under internal/adapters.
package ports

import (
	"context"
	"time"

	"orchestrator/internal/domain"
)

// RunRequest describes one invocation of an agent (PO or Dev).
type RunRequest struct {
	Prompt       string
	WorkDir      string
	AllowedTools []string
	MaxTurns     int
	Model        string

	// SkipPermissions, when true, tells the runner to bypass Claude Code's
	// interactive permission prompts (headless mode has no TTY to answer
	// them). Only ever set for Dev calls - see loop.go's stepDev and
	// claudecli's buildArgs for the reasoning.
	SkipPermissions bool

	// StreamOutput, when true, tells the runner to use `claude`'s
	// `--output-format stream-json` instead of the default `json`, reading
	// and summarizing each NDJSON event as it arrives instead of only
	// seeing output once the whole call finishes. Only set for Dev calls,
	// gated by Config.VerboseDevOutput (see loop.go's stepDev) - the PO's
	// prompt/response are short enough that streaming adds nothing.
	StreamOutput bool
}

// RunResult is what came back from an agent invocation.
type RunResult struct {
	Output      string
	Success     bool
	RateLimited bool
	ErrorMsg    string
	DurationSec float64
	NumTurns    int

	// ResetAt is when the current rate-limit window resets, if the CLI's
	// rate-limit message included that information and it could be parsed
	// (see claudecli's parseResetTime). Only ever set when RateLimited is
	// true; nil means the caller must fall back to a generic backoff
	// schedule instead (see orchestrator/loop.go's resolveRateLimitWait).
	ResetAt *time.Time
}

// AgentRunner invokes an autonomous coding agent (the Claude Code CLI, in
// production) and returns its result.
type AgentRunner interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// MemoryStore persists everything the orchestrator needs to remember
// between agent invocations and between process restarts.
type MemoryStore interface {
	ReadVision(ctx context.Context) (string, error)
	ReadState(ctx context.Context) (string, error)
	WriteState(ctx context.Context, content string) error
	ReadBacklog(ctx context.Context) ([]domain.Task, error)
	WriteBacklog(ctx context.Context, tasks []domain.Task) error
	AppendHistory(ctx context.Context, taskID string, content string) error
	LoadRunState(ctx context.Context) (*domain.RunState, error)
	SaveRunState(ctx context.Context, state *domain.RunState) error
}

// Notifier sends a human-readable message out of the loop (stdout, webhook,
// eventually WhatsApp, etc).
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// PushRequest describes one commit+push of the target repo's working tree.
type PushRequest struct {
	RepoDir       string
	CommitMessage string
}

// PushResult is what happened as a result of a PushRequest.
type PushResult struct {
	Pushed     bool
	CommitHash string
	Skipped    bool // true if there were no changes to commit
}

// GitPusher commits and pushes whatever changes are sitting in a repo's
// working tree - the only path by which code produced by the Dev agent
// reaches the CI/CD pipeline.
type GitPusher interface {
	// CommitAndPush runs `git add -A`, commits if there are changes, and
	// pushes. It returns Skipped=true (not an error) if there was nothing
	// to commit. Any real git failure (conflict, rejected push, missing
	// credentials) is returned as a non-nil error - it must never fail
	// silently.
	CommitAndPush(ctx context.Context, req PushRequest) (PushResult, error)
}

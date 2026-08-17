// Package ports declares the interfaces the orchestrator core depends on.
// Concrete implementations live under internal/adapters.
package ports

import (
	"context"

	"orchestrator/internal/domain"
)

// RunRequest describes one invocation of an agent (PO or Dev).
type RunRequest struct {
	Prompt       string
	WorkDir      string
	AllowedTools []string
	MaxTurns     int
	Model        string
}

// RunResult is what came back from an agent invocation.
type RunResult struct {
	Output      string
	Success     bool
	RateLimited bool
	ErrorMsg    string
	DurationSec float64
	NumTurns    int
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

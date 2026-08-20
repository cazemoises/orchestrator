// Package domain contains the core types shared by the orchestrator loop,
// independent of how they are persisted or how agents are invoked.
package domain

import "time"

// Phase identifies where the orchestrator loop is in its state machine.
type Phase string

const (
	PhaseIdle         Phase = "idle"
	PhasePODeciding   Phase = "po_deciding"
	PhaseDevPending   Phase = "dev_pending"
	PhaseDevRunning   Phase = "dev_running"
	PhaseDevDone      Phase = "dev_done"
	PhasePOEvaluating Phase = "po_evaluating"
	PhaseBlocked      Phase = "blocked"
	PhaseDone         Phase = "done"
)

// TaskStatus is the lifecycle status of a backlog Task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusDone       TaskStatus = "done"
	TaskStatusBlocked    TaskStatus = "blocked"
)

// Task is a single unit of work tracked in backlog.json.
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// DevReport is what the Dev agent produced for one invocation.
type DevReport struct {
	TaskID       string    `json:"task_id"`
	Prompt       string    `json:"prompt"`
	RawOutput    string    `json:"raw_output"`
	Success      bool      `json:"success"`
	ErrorMessage string    `json:"error_message,omitempty"`
	DurationSec  float64   `json:"duration_sec"`
	NumTurns     int       `json:"num_turns"`
	Timestamp    time.Time `json:"timestamp"`
}

// PODecision is the parsed response of the PO agent when deciding what to
// work on next.
type PODecision struct {
	Action string `json:"action"` // "next_task" | "product_done"
	TaskID string `json:"task_id"`

	// Title and Description are always required alongside TaskID, even when
	// the PO is just picking an existing backlog entry. This is what lets
	// the loop add TaskID to the backlog when it isn't found there yet -
	// e.g. the PO invented a new task_id because the backlog ran out -
	// instead of silently dropping the status update. See markTaskStatus.
	Title       string `json:"title"`
	Description string `json:"description"`

	DevPrompt string `json:"dev_prompt"`
	Reasoning string `json:"reasoning"`
}

// POEvaluation is the parsed response of the PO agent when evaluating a
// DevReport.
type POEvaluation struct {
	Verdict          string `json:"verdict"` // "accept" | "reject" | "block"
	Reasoning        string `json:"reasoning"`
	CorrectionPrompt string `json:"correction_prompt,omitempty"`
	StateUpdate      string `json:"state_update,omitempty"`
}

// RunState is the full checkpoint of the orchestrator loop. It is persisted
// after every meaningful transition so the loop can resume from the exact
// point it left off after a crash, VM reboot, or subscription rate limit.
type RunState struct {
	Phase            Phase      `json:"phase"`
	CurrentTaskID    string     `json:"current_task_id,omitempty"`
	CurrentDevPrompt string     `json:"current_dev_prompt,omitempty"`
	LastDevReport    *DevReport `json:"last_dev_report,omitempty"`
	CycleCount       int        `json:"cycle_count"`
	AttemptsOnTask   int        `json:"attempts_on_task"`
	RateLimitedAt    *time.Time `json:"rate_limited_at,omitempty"`
	LastCheckpoint   time.Time  `json:"last_checkpoint"`
}

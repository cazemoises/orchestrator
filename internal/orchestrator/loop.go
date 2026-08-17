// Package orchestrator implements the autonomous PO/Dev loop: a state
// machine that alternates between asking a Product Owner agent what to do
// next and asking a Developer agent to do it, persisting a checkpoint after
// every meaningful transition so it can resume exactly where it left off.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"orchestrator/internal/domain"
	"orchestrator/internal/ports"
)

// Config controls how the loop drives the two agents.
type Config struct {
	RepoDir            string
	MaxAttemptsPerTask int
	MaxTasksPerCycle   int // 0 = unlimited
	DevAllowedTools    []string
	DevMaxTurns        int
	POModel            string
	DevModel           string
	RateLimitWaitMin   time.Duration
	RateLimitWaitMax   time.Duration

	// LocalVerifyCommand runs (via `sh -c`, in RepoDir) right before a
	// commit+push, after the PO accepts a task. Empty skips verification
	// entirely (not recommended, but configurable). Example:
	// "go build ./... && go vet ./...".
	LocalVerifyCommand string

	// GitCommitPrefix prefixes the generated commit message. Defaults to
	// "feat: ".
	GitCommitPrefix string
}

func (c *Config) applyDefaults() {
	if c.MaxAttemptsPerTask == 0 {
		c.MaxAttemptsPerTask = 3
	}
	if c.DevMaxTurns == 0 {
		c.DevMaxTurns = 30
	}
	if c.RateLimitWaitMin == 0 {
		c.RateLimitWaitMin = 10 * time.Minute
	}
	if c.RateLimitWaitMax == 0 {
		c.RateLimitWaitMax = 90 * time.Minute
	}
	if c.GitCommitPrefix == "" {
		c.GitCommitPrefix = "feat: "
	}
}

// Loop is the PO/Dev state machine.
type Loop struct {
	Agent    ports.AgentRunner
	Store    ports.MemoryStore
	Notifier ports.Notifier
	Pusher   ports.GitPusher
	Config   Config

	// Now, Sleep and Verify are seams for testing; production code leaves
	// them at their New()-assigned defaults.
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) error
	Verify func(ctx context.Context, dir, command string) (stdout, stderr string, err error)
}

// New creates a Loop with defaults applied to Config and real time/sleep/verify.
func New(agent ports.AgentRunner, store ports.MemoryStore, notifier ports.Notifier, pusher ports.GitPusher, cfg Config) *Loop {
	cfg.applyDefaults()
	return &Loop{
		Agent:    agent,
		Store:    store,
		Notifier: notifier,
		Pusher:   pusher,
		Config:   cfg,
		Now:      time.Now,
		Sleep:    realSleep,
		Verify:   realVerify,
	}
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// realVerify runs command through a shell in dir - used to sanity-check the
// repo (e.g. "go build ./... && go vet ./...") before the loop ever commits
// and pushes code produced by the Dev agent.
func realVerify(ctx context.Context, dir, command string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// computeWait implements the resumable exponential backoff: given how much
// time has already elapsed since the rate limit was first observed, it
// returns how much longer to wait before the next retry. The schedule
// (min, 2*min, 4*min, ... capped at max) is derived purely from elapsed
// time, so it produces the correct remaining wait even if the process
// restarted mid-wait and only knows the original timestamp.
func computeWait(elapsed, min, max time.Duration) time.Duration {
	step := min
	var cumulative time.Duration
	for {
		cumulative += step
		if elapsed < cumulative {
			return cumulative - elapsed
		}
		step *= 2
		if step > max {
			step = max
		}
	}
}

// Run drives the state machine until the product is done, a task gets
// blocked, or ctx is cancelled. It always returns with the current
// RunState checkpointed to the store.
func (l *Loop) Run(ctx context.Context) error {
	state, err := l.Store.LoadRunState(ctx)
	if err != nil {
		return err
	}
	if state.Phase == domain.PhaseIdle {
		state.Phase = domain.PhasePODeciding
	}

	tasksCompleted := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		switch state.Phase {
		case domain.PhaseDone, domain.PhaseBlocked:
			return l.checkpoint(ctx, state)

		case domain.PhasePODeciding:
			if l.Config.MaxTasksPerCycle > 0 && tasksCompleted >= l.Config.MaxTasksPerCycle {
				return l.checkpoint(ctx, state)
			}
			if err := l.stepPODecide(ctx, state); err != nil {
				return err
			}

		case domain.PhaseDevPending, domain.PhaseDevRunning:
			if err := l.stepDev(ctx, state); err != nil {
				return err
			}

		case domain.PhaseDevDone, domain.PhasePOEvaluating:
			completed, err := l.stepPOEvaluate(ctx, state)
			if err != nil {
				return err
			}
			if completed {
				tasksCompleted++
			}

		default:
			return fmt.Errorf("orchestrator: unknown phase %q", state.Phase)
		}
	}
}

func (l *Loop) checkpoint(ctx context.Context, state *domain.RunState) error {
	state.LastCheckpoint = l.Now()
	return l.Store.SaveRunState(ctx, state)
}

// callAgent invokes req and transparently retries on rate limiting: it
// checkpoints RateLimitedAt on first detection (only), notifies, sleeps the
// computed backoff, and retries the exact same request - never advancing
// the caller's phase and never losing what was already checkpointed.
func (l *Loop) callAgent(ctx context.Context, state *domain.RunState, req ports.RunRequest) (ports.RunResult, error) {
	for {
		res, err := l.Agent.Run(ctx, req)
		if err != nil {
			return ports.RunResult{}, err
		}

		if !res.RateLimited {
			if state.RateLimitedAt != nil {
				state.RateLimitedAt = nil
				if err := l.checkpoint(ctx, state); err != nil {
					return ports.RunResult{}, err
				}
			}
			return res, nil
		}

		now := l.Now()
		if state.RateLimitedAt == nil {
			state.RateLimitedAt = &now
			if err := l.checkpoint(ctx, state); err != nil {
				return ports.RunResult{}, err
			}
		}

		elapsed := now.Sub(*state.RateLimitedAt)
		wait := computeWait(elapsed, l.Config.RateLimitWaitMin, l.Config.RateLimitWaitMax)
		_ = l.Notifier.Notify(ctx, fmt.Sprintf(
			"Claude Code rate limited; waiting %s before retrying (rate-limited since %s)",
			wait, state.RateLimitedAt.Format(time.RFC3339)))

		if err := l.Sleep(ctx, wait); err != nil {
			return ports.RunResult{}, err
		}
	}
}

func (l *Loop) stepPODecide(ctx context.Context, state *domain.RunState) error {
	vision, err := l.Store.ReadVision(ctx)
	if err != nil {
		return err
	}
	stateMD, err := l.Store.ReadState(ctx)
	if err != nil {
		return err
	}
	tasks, err := l.Store.ReadBacklog(ctx)
	if err != nil {
		return err
	}
	backlogJSON, err := json.Marshal(tasks)
	if err != nil {
		return err
	}

	req := ports.RunRequest{
		Prompt:  poDecidePrompt(vision, stateMD, string(backlogJSON)),
		WorkDir: l.Config.RepoDir,
		Model:   l.Config.POModel,
	}
	res, err := l.callAgent(ctx, state, req)
	if err != nil {
		return err
	}

	decision, err := parsePODecision(res.Output)
	if err != nil {
		_ = l.Notifier.Notify(ctx, fmt.Sprintf("PO decision could not be parsed, blocking: %v", err))
		state.Phase = domain.PhaseBlocked
		return l.checkpoint(ctx, state)
	}

	if decision.Action == "product_done" {
		state.Phase = domain.PhaseDone
		return l.checkpoint(ctx, state)
	}

	if err := markTaskStatus(tasks, decision.TaskID, domain.TaskStatusInProgress, l.Now()); err == nil {
		if err := l.Store.WriteBacklog(ctx, tasks); err != nil {
			return err
		}
	}

	state.CurrentTaskID = decision.TaskID
	state.CurrentDevPrompt = decision.DevPrompt
	state.AttemptsOnTask = 0
	state.LastDevReport = nil
	state.CycleCount++
	state.Phase = domain.PhaseDevPending
	return l.checkpoint(ctx, state)
}

func (l *Loop) stepDev(ctx context.Context, state *domain.RunState) error {
	// Checkpoint BEFORE calling the Dev agent: if the process dies mid-call,
	// resuming sees Phase=dev_running and knows to re-issue this exact call.
	state.Phase = domain.PhaseDevRunning
	if err := l.checkpoint(ctx, state); err != nil {
		return err
	}

	req := ports.RunRequest{
		Prompt:       state.CurrentDevPrompt,
		WorkDir:      l.Config.RepoDir,
		AllowedTools: l.Config.DevAllowedTools,
		MaxTurns:     l.Config.DevMaxTurns,
		Model:        l.Config.DevModel,
	}
	res, err := l.callAgent(ctx, state, req)
	if err != nil {
		return err
	}

	state.LastDevReport = &domain.DevReport{
		TaskID:       state.CurrentTaskID,
		Prompt:       state.CurrentDevPrompt,
		RawOutput:    res.Output,
		Success:      res.Success,
		ErrorMessage: res.ErrorMsg,
		DurationSec:  res.DurationSec,
		NumTurns:     res.NumTurns,
		Timestamp:    l.Now(),
	}
	state.Phase = domain.PhaseDevDone
	return l.checkpoint(ctx, state)
}

func (l *Loop) stepPOEvaluate(ctx context.Context, state *domain.RunState) (bool, error) {
	state.Phase = domain.PhasePOEvaluating
	if err := l.checkpoint(ctx, state); err != nil {
		return false, err
	}

	vision, err := l.Store.ReadVision(ctx)
	if err != nil {
		return false, err
	}
	stateMD, err := l.Store.ReadState(ctx)
	if err != nil {
		return false, err
	}
	reportJSON, err := json.Marshal(state.LastDevReport)
	if err != nil {
		return false, err
	}

	req := ports.RunRequest{
		Prompt:  poEvaluatePrompt(vision, stateMD, string(reportJSON)),
		WorkDir: l.Config.RepoDir,
		Model:   l.Config.POModel,
	}
	res, err := l.callAgent(ctx, state, req)
	if err != nil {
		return false, err
	}

	taskID := state.CurrentTaskID

	evaluation, err := parsePOEvaluation(res.Output)
	if err != nil {
		_ = l.Notifier.Notify(ctx, fmt.Sprintf("PO evaluation could not be parsed, blocking: %v", err))
		state.Phase = domain.PhaseBlocked
		return false, l.checkpoint(ctx, state)
	}

	_ = l.Store.AppendHistory(ctx, taskID, formatHistoryEntry(state, evaluation, l.Now()))

	if evaluation.StateUpdate != "" {
		if err := l.Store.WriteState(ctx, evaluation.StateUpdate); err != nil {
			return false, err
		}
	}

	switch evaluation.Verdict {
	case "accept":
		return l.handleAccept(ctx, state, taskID, evaluation)

	case "reject":
		state.AttemptsOnTask++
		if state.AttemptsOnTask >= l.Config.MaxAttemptsPerTask {
			return false, l.blockTask(ctx, state, taskID, fmt.Sprintf(
				"Task %s blocked after %d failed attempts. Last reasoning: %s",
				taskID, state.AttemptsOnTask, evaluation.Reasoning))
		}
		state.CurrentDevPrompt = evaluation.CorrectionPrompt
		state.Phase = domain.PhaseDevPending
		return false, l.checkpoint(ctx, state)

	case "block":
		return false, l.blockTask(ctx, state, taskID, fmt.Sprintf(
			"Task %s blocked by PO: %s", taskID, evaluation.Reasoning))

	default:
		return false, l.blockTask(ctx, state, taskID, fmt.Sprintf(
			"Task %s blocked: PO returned unknown verdict %q", taskID, evaluation.Verdict))
	}
}

// handleAccept runs the guardrails between a PO "accept" verdict and
// actually completing the task: a local sanity check (if configured), then
// a commit+push - the only path by which code reaches the CI/CD pipeline
// now that the orchestrator runs locally instead of on the VM. A failing
// verify is treated as an automatic reject (no extra PO call, deterministic
// correction prompt); a failing push blocks the task for human intervention
// - git conflicts and rejected pushes are not something the loop tries to
// resolve on its own.
func (l *Loop) handleAccept(ctx context.Context, state *domain.RunState, taskID string, evaluation domain.POEvaluation) (bool, error) {
	tasks, err := l.Store.ReadBacklog(ctx)
	if err != nil {
		return false, err
	}
	taskTitle := taskID
	for _, task := range tasks {
		if task.ID == taskID {
			taskTitle = task.Title
			break
		}
	}

	if l.Config.LocalVerifyCommand != "" {
		stdout, stderr, verifyErr := l.Verify(ctx, l.Config.RepoDir, l.Config.LocalVerifyCommand)
		if verifyErr != nil {
			return false, l.autoRejectFromVerifyFailure(ctx, state, taskID, stdout, stderr, verifyErr)
		}
	}

	commitMessage := fmt.Sprintf("%s%s\n\n%s", l.Config.GitCommitPrefix, taskTitle, evaluation.Reasoning)
	pushRes, pushErr := l.Pusher.CommitAndPush(ctx, ports.PushRequest{
		RepoDir:       l.Config.RepoDir,
		CommitMessage: commitMessage,
	})
	_ = l.Store.AppendHistory(ctx, taskID, formatPushHistory(pushRes, pushErr, l.Now()))
	if pushErr != nil {
		return false, l.blockTask(ctx, state, taskID, fmt.Sprintf(
			"Task %s was accepted but git push failed - needs human intervention (merge conflict, protected branch, or missing credentials): %v",
			taskID, pushErr))
	}

	if err := markTaskStatus(tasks, taskID, domain.TaskStatusDone, l.Now()); err == nil {
		if err := l.Store.WriteBacklog(ctx, tasks); err != nil {
			return false, err
		}
	}
	state.Phase = domain.PhasePODeciding
	state.CurrentTaskID = ""
	state.CurrentDevPrompt = ""
	state.AttemptsOnTask = 0
	state.LastDevReport = nil
	return true, l.checkpoint(ctx, state)
}

// autoRejectFromVerifyFailure mirrors the PO's own reject handling
// (AttemptsOnTask / correction_prompt / eventual block) but is triggered
// deterministically by a failing LocalVerifyCommand, without spending an
// extra PO call on a decision the command's exit code already answered.
func (l *Loop) autoRejectFromVerifyFailure(ctx context.Context, state *domain.RunState, taskID, stdout, stderr string, verifyErr error) error {
	state.AttemptsOnTask++

	correction := fmt.Sprintf(
		"Automated local verification failed before this task could be committed and pushed. "+
			"Fix the underlying problem - do not just work around the check.\n\n"+
			"Command: %s\n\nError: %v\n\nstdout:\n%s\n\nstderr:\n%s\n",
		l.Config.LocalVerifyCommand, verifyErr, stdout, stderr)

	_ = l.Store.AppendHistory(ctx, taskID, fmt.Sprintf(
		"## %s\n\n- verdict: reject (automatic - local verify failed)\n- attempts_on_task: %d\n- command: %s\n\n",
		l.Now().Format(time.RFC3339), state.AttemptsOnTask, l.Config.LocalVerifyCommand))

	if state.AttemptsOnTask >= l.Config.MaxAttemptsPerTask {
		return l.blockTask(ctx, state, taskID, fmt.Sprintf(
			"Task %s blocked after %d attempts (last failure: automated local verification command %q).",
			taskID, state.AttemptsOnTask, l.Config.LocalVerifyCommand))
	}

	state.CurrentDevPrompt = correction
	state.Phase = domain.PhaseDevPending
	return l.checkpoint(ctx, state)
}

func formatPushHistory(res ports.PushResult, err error, now time.Time) string {
	if err != nil {
		return fmt.Sprintf("## %s\n\n- push: FAILED\n- error: %v\n\n", now.Format(time.RFC3339), err)
	}
	if res.Skipped {
		return fmt.Sprintf("## %s\n\n- push: skipped (no changes to commit)\n\n", now.Format(time.RFC3339))
	}
	return fmt.Sprintf("## %s\n\n- push: pushed commit %s\n\n", now.Format(time.RFC3339), res.CommitHash)
}

func (l *Loop) blockTask(ctx context.Context, state *domain.RunState, taskID, message string) error {
	tasks, err := l.Store.ReadBacklog(ctx)
	if err == nil {
		if err := markTaskStatus(tasks, taskID, domain.TaskStatusBlocked, l.Now()); err == nil {
			_ = l.Store.WriteBacklog(ctx, tasks)
		}
	}
	_ = l.Notifier.Notify(ctx, message)
	state.Phase = domain.PhaseBlocked
	return l.checkpoint(ctx, state)
}

func markTaskStatus(tasks []domain.Task, taskID string, status domain.TaskStatus, now time.Time) error {
	for i := range tasks {
		if tasks[i].ID == taskID {
			tasks[i].Status = status
			tasks[i].UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("orchestrator: task %q not found in backlog", taskID)
}

func formatHistoryEntry(state *domain.RunState, eval domain.POEvaluation, now time.Time) string {
	return fmt.Sprintf(
		"## %s\n\n- verdict: %s\n- attempts_on_task: %d\n- reasoning: %s\n\n",
		now.Format(time.RFC3339), eval.Verdict, state.AttemptsOnTask, eval.Reasoning)
}

func parsePODecision(raw string) (domain.PODecision, error) {
	js, err := extractJSON(raw)
	if err != nil {
		return domain.PODecision{}, err
	}
	var d domain.PODecision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return domain.PODecision{}, err
	}
	return d, nil
}

func parsePOEvaluation(raw string) (domain.POEvaluation, error) {
	js, err := extractJSON(raw)
	if err != nil {
		return domain.POEvaluation{}, err
	}
	var e domain.POEvaluation
	if err := json.Unmarshal([]byte(js), &e); err != nil {
		return domain.POEvaluation{}, err
	}
	return e, nil
}

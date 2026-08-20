// Package orchestrator implements the autonomous PO/Dev loop: a state
// machine that alternates between asking a Product Owner agent what to do
// next and asking a Developer agent to do it, persisting a checkpoint after
// every meaningful transition so it can resume exactly where it left off.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if len(c.DevAllowedTools) == 0 {
		// Write is required, not optional: without it the Dev agent can only
		// edit files that already exist, which breaks any task that starts
		// from scratch (e.g. creating the first domain entities).
		c.DevAllowedTools = []string{"Read", "Edit", "Write", "Bash"}
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

// buildVerifyCommand picks the right shell for LocalVerifyCommand based on
// goos (passed explicitly, never read from runtime.GOOS internally, so
// tests can exercise both branches on one machine). On Windows, `sh` is
// frequently not on PATH even with Git for Windows installed (only
// Git\cmd, not Git\bin, ends up there by default) - cmd.exe is always
// present, so that's what we use there instead.
func buildVerifyCommand(ctx context.Context, goos, command string) *exec.Cmd {
	if goos == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// realVerify runs command through a shell in dir - used to sanity-check the
// repo (e.g. "go build ./... && go vet ./...") before the loop ever commits
// and pushes code produced by the Dev agent.
func realVerify(ctx context.Context, dir, command string) (string, string, error) {
	cmd := buildVerifyCommand(ctx, runtime.GOOS, command)
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
		log.Printf("orchestrator: rate limited - esperando mais %s antes de tentar de novo (limitado desde %s, %s decorrido até agora)",
			wait, state.RateLimitedAt.Format(time.RFC3339), elapsed.Round(time.Second))
		_ = l.Notifier.Notify(ctx, fmt.Sprintf(
			"Claude Code rate limited; waiting %s before retrying (rate-limited since %s)",
			wait, state.RateLimitedAt.Format(time.RFC3339)))

		if err := l.Sleep(ctx, wait); err != nil {
			return ports.RunResult{}, err
		}
	}
}

func (l *Loop) stepPODecide(ctx context.Context, state *domain.RunState) error {
	log.Printf("orchestrator: PO analisando backlog e decidindo próxima tarefa...")

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
		// TODO: remover depois de diagnosticar o problema de parse do PO
		writeDebugLastFailure(req.Prompt, res.Output, l.Now())

		_ = l.Notifier.Notify(ctx, fmt.Sprintf("PO decision could not be parsed, blocking: %v", err))
		state.Phase = domain.PhaseBlocked
		return l.checkpoint(ctx, state)
	}

	if decision.Action == "product_done" {
		log.Printf("orchestrator: PO decidiu que o produto está pronto\nreasoning: %s", decision.Reasoning)
		state.Phase = domain.PhaseDone
		return l.checkpoint(ctx, state)
	}

	log.Printf("orchestrator: PO decidiu: task=%s título=%q\nreasoning: %s", decision.TaskID, decision.Title, decision.Reasoning)

	tasks = markTaskStatus(tasks, decision.TaskID, decision.Title, decision.Description, domain.TaskStatusInProgress, l.Now())
	if err := l.Store.WriteBacklog(ctx, tasks); err != nil {
		return err
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

	taskTitle := state.CurrentTaskID
	if tasks, err := l.Store.ReadBacklog(ctx); err == nil {
		for _, task := range tasks {
			if task.ID == state.CurrentTaskID {
				taskTitle = task.Title
				break
			}
		}
	}
	log.Printf("orchestrator: Dev iniciando tarefa %s: %s (prompt: %d caracteres)",
		state.CurrentTaskID, taskTitle, len(state.CurrentDevPrompt))

	req := ports.RunRequest{
		Prompt:       state.CurrentDevPrompt,
		WorkDir:      l.Config.RepoDir,
		AllowedTools: l.Config.DevAllowedTools,
		MaxTurns:     l.Config.DevMaxTurns,
		Model:        l.Config.DevModel,
		// Headless (-p) has no TTY to answer Claude Code's interactive
		// permission prompts - see claudecli's buildArgs for the full
		// safety reasoning. Only the Dev call sets this; the PO never
		// touches files, so it keeps normal permission prompts.
		SkipPermissions: true,
	}
	res, err := l.callAgent(ctx, state, req)
	if err != nil {
		return err
	}

	log.Printf("orchestrator: Dev terminou tarefa %s: success=%v duração=%.1fs num_turns=%d",
		state.CurrentTaskID, res.Success, res.DurationSec, res.NumTurns)

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
	log.Printf("orchestrator: PO avaliando o relatório do Dev...")

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

	log.Printf("orchestrator: PO avaliou task=%s: veredito=%s\nreasoning: %s", taskID, evaluation.Verdict, evaluation.Reasoning)

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

	// TODO: remover depois de confirmar que o ciclo completo accept->push funciona
	writeDebugPushLog(taskID, pushRes, pushErr, l.Now())

	_ = l.Store.AppendHistory(ctx, taskID, formatPushHistory(pushRes, pushErr, l.Now()))
	if pushErr != nil {
		return false, l.blockTask(ctx, state, taskID, fmt.Sprintf(
			"Task %s was accepted but git push failed - needs human intervention (merge conflict, protected branch, or missing credentials): %v",
			taskID, pushErr))
	}

	tasks = markTaskStatus(tasks, taskID, taskTitle, "", domain.TaskStatusDone, l.Now())
	if err := l.Store.WriteBacklog(ctx, tasks); err != nil {
		return false, err
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
		tasks = markTaskStatus(tasks, taskID, taskID, "", domain.TaskStatusBlocked, l.Now())
		_ = l.Store.WriteBacklog(ctx, tasks)
	}
	_ = l.Notifier.Notify(ctx, message)
	state.Phase = domain.PhaseBlocked
	return l.checkpoint(ctx, state)
}

// markTaskStatus updates taskID's status in tasks. If taskID isn't found -
// e.g. the PO invented a task_id because the backlog ran out (see
// poDecidePrompt) - it is appended instead of the update being silently
// dropped, using title/description as provided by the caller (typically the
// PO's own decision). The returned slice must be passed to WriteBacklog:
// appending may reallocate, so tasks itself cannot be relied on afterward.
func markTaskStatus(tasks []domain.Task, taskID, title, description string, status domain.TaskStatus, now time.Time) []domain.Task {
	for i := range tasks {
		if tasks[i].ID == taskID {
			tasks[i].Status = status
			tasks[i].UpdatedAt = now
			return tasks
		}
	}
	return append(tasks, domain.Task{
		ID:          taskID,
		Title:       title,
		Description: description,
		Status:      status,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
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

// TODO: remover depois de diagnosticar o problema de parse do PO
//
// writeDebugLastFailure dumps the prompt and raw output of a failed PO
// decision parse to data/debug_last_failure.txt, overwriting it each time.
// Best-effort only: a failure to write here must never affect the loop.
func writeDebugLastFailure(prompt, output string, now time.Time) {
	content := fmt.Sprintf(
		"timestamp: %s\n\n--- prompt ---\n%s\n\n--- raw output ---\n%s\n",
		now.Format(time.RFC3339), prompt, output)

	if err := os.MkdirAll("data", 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join("data", "debug_last_failure.txt"), []byte(content), 0o644)
}

// TODO: remover depois de confirmar que o ciclo completo accept->push funciona
//
// writeDebugPushLog appends one line per CommitAndPush invocation to
// data/debug_push_log.txt (append, not overwrite - the point is a trail
// across the whole run), so a real run leaves an unambiguous record of
// every push attempt and its outcome (added while investigating task-001
// getting stuck with unpushed local commits - see the commit that added
// this). Best-effort only: a failure to write here must never affect the
// loop.
func writeDebugPushLog(taskID string, res ports.PushResult, pushErr error, now time.Time) {
	var outcome string
	switch {
	case pushErr != nil:
		outcome = fmt.Sprintf("ERROR: %v", pushErr)
	case res.Skipped:
		outcome = "SKIPPED (no changes to commit)"
	case res.Pushed:
		outcome = fmt.Sprintf("PUSHED commit %s", res.CommitHash)
	default:
		outcome = fmt.Sprintf("UNEXPECTED result: %+v", res)
	}
	line := fmt.Sprintf("[%s] CommitAndPush task=%s -> %s\n", now.Format(time.RFC3339), taskID, outcome)

	if err := os.MkdirAll("data", 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join("data", "debug_push_log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

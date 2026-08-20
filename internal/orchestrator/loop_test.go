package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"orchestrator/internal/domain"
	"orchestrator/internal/ports"
)

// ---------------------------------------------------------------------
// computeWait (pure backoff function)
// ---------------------------------------------------------------------

func TestComputeWait_FirstWaitEqualsMin(t *testing.T) {
	got := computeWait(0, 10*time.Minute, 90*time.Minute)
	if got != 10*time.Minute {
		t.Fatalf("got %s, want 10m", got)
	}
}

func TestComputeWait_DoublesEachRound(t *testing.T) {
	// After exactly one Min has elapsed, the next wait should be 2*Min.
	got := computeWait(10*time.Minute, 10*time.Minute, 90*time.Minute)
	if got != 20*time.Minute {
		t.Fatalf("got %s, want 20m", got)
	}
}

func TestComputeWait_NeverExceedsMax(t *testing.T) {
	got := computeWait(10000*time.Minute, 10*time.Minute, 90*time.Minute)
	if got <= 0 || got > 90*time.Minute {
		t.Fatalf("got %s, want a positive wait capped at 90m", got)
	}
}

func TestComputeWait_MidRoundReturnsRemainder(t *testing.T) {
	// 7 minutes into a 10-minute first wait: 3 minutes remain.
	got := computeWait(7*time.Minute, 10*time.Minute, 90*time.Minute)
	if got != 3*time.Minute {
		t.Fatalf("got %s, want 3m", got)
	}
}

// ---------------------------------------------------------------------
// buildVerifyCommand (portable shell selection for LocalVerifyCommand)
// ---------------------------------------------------------------------

func TestBuildVerifyCommand_UsesCmdOnWindows(t *testing.T) {
	cmd := buildVerifyCommand(context.Background(), "windows", "go build ./...")
	want := []string{"cmd", "/C", "go build ./..."}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got args %v, want %v", cmd.Args, want)
	}
}

func TestBuildVerifyCommand_UsesShOnLinux(t *testing.T) {
	cmd := buildVerifyCommand(context.Background(), "linux", "go build ./...")
	want := []string{"sh", "-c", "go build ./..."}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got args %v, want %v", cmd.Args, want)
	}
}

func TestBuildVerifyCommand_UsesShOnDarwin(t *testing.T) {
	cmd := buildVerifyCommand(context.Background(), "darwin", "go build ./...")
	want := []string{"sh", "-c", "go build ./..."}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got args %v, want %v", cmd.Args, want)
	}
}

// ---------------------------------------------------------------------
// Config.applyDefaults
// ---------------------------------------------------------------------

func TestApplyDefaults_DefaultsDevAllowedToolsWhenEmpty(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()

	joined := strings.Join(cfg.DevAllowedTools, ",")
	for _, want := range []string{"Read", "Edit", "Write", "Bash"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("default DevAllowedTools %v missing %q", cfg.DevAllowedTools, want)
		}
	}
}

func TestApplyDefaults_DoesNotOverrideExplicitDevAllowedTools(t *testing.T) {
	cfg := Config{DevAllowedTools: []string{"Bash"}}
	cfg.applyDefaults()

	if len(cfg.DevAllowedTools) != 1 || cfg.DevAllowedTools[0] != "Bash" {
		t.Fatalf("got %v, want explicit DevAllowedTools left untouched", cfg.DevAllowedTools)
	}
}

// ---------------------------------------------------------------------
// test fakes
// ---------------------------------------------------------------------

type fakeStore struct {
	vision       string
	state        string
	backlog      []domain.Task
	runState     *domain.RunState
	history      map[string][]string
	savedPhases  []domain.Phase
	backlogSnaps [][]domain.Task
	log          *[]string
}

func newFakeStore(log *[]string) *fakeStore {
	return &fakeStore{
		runState: &domain.RunState{Phase: domain.PhaseIdle},
		history:  map[string][]string{},
		log:      log,
	}
}

func (s *fakeStore) ReadVision(ctx context.Context) (string, error) { return s.vision, nil }
func (s *fakeStore) ReadState(ctx context.Context) (string, error)  { return s.state, nil }
func (s *fakeStore) WriteState(ctx context.Context, content string) error {
	s.state = content
	return nil
}
func (s *fakeStore) ReadBacklog(ctx context.Context) ([]domain.Task, error) {
	return append([]domain.Task{}, s.backlog...), nil
}
func (s *fakeStore) WriteBacklog(ctx context.Context, tasks []domain.Task) error {
	s.backlog = append([]domain.Task{}, tasks...)
	s.backlogSnaps = append(s.backlogSnaps, s.backlog)
	return nil
}
func (s *fakeStore) AppendHistory(ctx context.Context, taskID string, content string) error {
	s.history[taskID] = append(s.history[taskID], content)
	return nil
}
func (s *fakeStore) LoadRunState(ctx context.Context) (*domain.RunState, error) {
	cp := *s.runState
	return &cp, nil
}
func (s *fakeStore) SaveRunState(ctx context.Context, state *domain.RunState) error {
	cp := *state
	s.runState = &cp
	s.savedPhases = append(s.savedPhases, state.Phase)
	if s.log != nil {
		*s.log = append(*s.log, "save:"+string(state.Phase))
	}
	return nil
}

var _ ports.MemoryStore = (*fakeStore)(nil)

type fakeAgent struct {
	calls   []ports.RunRequest
	respond func(idx int, req ports.RunRequest) (ports.RunResult, error)
	log     *[]string
}

func (a *fakeAgent) Run(ctx context.Context, req ports.RunRequest) (ports.RunResult, error) {
	idx := len(a.calls)
	a.calls = append(a.calls, req)
	if a.log != nil {
		*a.log = append(*a.log, fmt.Sprintf("agent:%d", idx))
	}
	return a.respond(idx, req)
}

var _ ports.AgentRunner = (*fakeAgent)(nil)

type fakeNotifier struct {
	messages []string
}

func (n *fakeNotifier) Notify(ctx context.Context, message string) error {
	n.messages = append(n.messages, message)
	return nil
}

var _ ports.Notifier = (*fakeNotifier)(nil)

type fakePusher struct {
	calls  []ports.PushRequest
	result ports.PushResult
	err    error
}

func (p *fakePusher) CommitAndPush(ctx context.Context, req ports.PushRequest) (ports.PushResult, error) {
	p.calls = append(p.calls, req)
	return p.result, p.err
}

var _ ports.GitPusher = (*fakePusher)(nil)

func newSuccessPusher() *fakePusher {
	return &fakePusher{result: ports.PushResult{Pushed: true, CommitHash: "abc123"}}
}

func testConfig() Config {
	return Config{
		RepoDir:            "/repo",
		MaxAttemptsPerTask: 3,
		DevAllowedTools:    []string{"Bash", "Edit"},
		DevMaxTurns:        30,
		POModel:            "opus",
		DevModel:           "sonnet",
		RateLimitWaitMin:   10 * time.Minute,
		RateLimitWaitMax:   90 * time.Minute,
	}
}

func newTestLoop(agent ports.AgentRunner, store ports.MemoryStore, notifier ports.Notifier, pusher ports.GitPusher, cfg Config) *Loop {
	l := New(agent, store, notifier, pusher, cfg)
	l.Now = func() time.Time { return time.Unix(1700000000, 0) }
	l.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
	return l
}

// ---------------------------------------------------------------------
// phase transitions
// ---------------------------------------------------------------------

func TestRun_HappyPath_AcceptsTaskThenReportsProductDone(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"implement thing","reasoning":"go"}`}, nil
		case 1:
			return ports.RunResult{Output: "done implementing", Success: true, NumTurns: 5, DurationSec: 12.3}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"tests pass","state_update":"new state text"}`}, nil
		case 3:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"nothing left"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d: %+v", idx, req)
			return ports.RunResult{}, nil
		}
	}

	pusher := newSuccessPusher()
	loop := newTestLoop(agent, store, notifier, pusher, testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if store.runState.Phase != domain.PhaseDone {
		t.Fatalf("got phase %q, want done", store.runState.Phase)
	}
	if len(store.backlog) != 1 || store.backlog[0].Status != domain.TaskStatusDone {
		t.Fatalf("got backlog %+v, want t1 done", store.backlog)
	}
	if store.state != "new state text" {
		t.Fatalf("got state %q, want state_update to have been written", store.state)
	}
	if len(agent.calls) != 4 {
		t.Fatalf("got %d agent calls, want 4", len(agent.calls))
	}
	if agent.calls[0].Model != "opus" {
		t.Fatalf("PO call model = %q, want opus", agent.calls[0].Model)
	}
	if agent.calls[1].Model != "sonnet" || agent.calls[1].MaxTurns != 30 || strings.Join(agent.calls[1].AllowedTools, ",") != "Bash,Edit" {
		t.Fatalf("dev call = %+v, want sonnet/30/Bash,Edit", agent.calls[1])
	}
	if len(store.history["t1"]) == 0 {
		t.Fatal("expected a history entry to be recorded for t1")
	}
	if len(pusher.calls) != 1 {
		t.Fatalf("got %d push calls, want exactly 1 (after the accept)", len(pusher.calls))
	}
	if pusher.calls[0].RepoDir != testConfig().RepoDir {
		t.Fatalf("push RepoDir = %q, want %q", pusher.calls[0].RepoDir, testConfig().RepoDir)
	}
	if !strings.Contains(pusher.calls[0].CommitMessage, "First task") || !strings.Contains(pusher.calls[0].CommitMessage, "tests pass") {
		t.Fatalf("commit message = %q, want it to include the task title and PO reasoning", pusher.calls[0].CommitMessage)
	}
}

func TestRun_MarksTaskInProgressWhenDevStarts(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"do it"}`}, nil
		default:
			// Block forever after the decide call so we can inspect state
			// right as the dev phase is about to start.
			return ports.RunResult{Output: "irrelevant", Success: true}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	_ = loop.Run(context.Background())

	if len(store.backlogSnaps) == 0 {
		t.Fatal("expected at least one backlog write")
	}
	first := store.backlogSnaps[0]
	if first[0].Status != domain.TaskStatusInProgress {
		t.Fatalf("got first backlog snapshot status %q, want in_progress", first[0].Status)
	}
}

func TestRun_RejectUsesCorrectionPromptForNextDevCall(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "attempt1", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"reject","reasoning":"nope","correction_prompt":"fix X"}`}, nil
		case 3:
			return ports.RunResult{Output: "attempt2", Success: true}, nil
		case 4:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"ok now"}`}, nil
		case 5:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"done"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if agent.calls[3].Prompt != "fix X" {
		t.Fatalf("second dev call prompt = %q, want correction_prompt %q", agent.calls[3].Prompt, "fix X")
	}
	if store.backlog[0].Status != domain.TaskStatusDone {
		t.Fatalf("got status %q, want done after eventual accept", store.backlog[0].Status)
	}
}

func TestRun_RejectUntilMaxAttempts_BlocksTaskAndStops(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}
	cfg := testConfig()
	cfg.MaxAttemptsPerTask = 2

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "attempt1", Success: false}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"reject","reasoning":"nope","correction_prompt":"fix1"}`}, nil
		case 3:
			return ports.RunResult{Output: "attempt2", Success: false}, nil
		case 4:
			return ports.RunResult{Output: `{"verdict":"reject","reasoning":"still nope","correction_prompt":"fix2"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d: loop should have stopped at MaxAttemptsPerTask", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), cfg)
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if store.runState.Phase != domain.PhaseBlocked {
		t.Fatalf("got phase %q, want blocked", store.runState.Phase)
	}
	if store.backlog[0].Status != domain.TaskStatusBlocked {
		t.Fatalf("got status %q, want blocked", store.backlog[0].Status)
	}
	if len(agent.calls) != 5 {
		t.Fatalf("got %d agent calls, want exactly 5 (loop must stop, not retry a 3rd time)", len(agent.calls))
	}
	if len(notifier.messages) == 0 {
		t.Fatal("expected a notification when a task gets blocked")
	}
}

func TestRun_BlockVerdict_StopsImmediately(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "attempt1", Success: false}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"block","reasoning":"needs a human"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if store.runState.Phase != domain.PhaseBlocked {
		t.Fatalf("got phase %q, want blocked", store.runState.Phase)
	}
	if store.backlog[0].Status != domain.TaskStatusBlocked {
		t.Fatalf("got status %q, want blocked", store.backlog[0].Status)
	}
	if len(notifier.messages) == 0 {
		t.Fatal("expected a notification on block verdict")
	}
}

// ---------------------------------------------------------------------
// guardrails: local verify + git push on accept
// ---------------------------------------------------------------------

func TestRun_AcceptWithFailingLocalVerify_AutoRejectsWithoutCallingPOAgain(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}
	pusher := newSuccessPusher()

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "impl1", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"looks good"}`}, nil
		case 3:
			// This must be the retried DEV call (using our own deterministic
			// correction prompt), NOT another PO call - proves the
			// auto-reject never asked the PO anything.
			if req.Prompt == "p1" {
				t.Fatalf("dev call #%d reused the original prompt %q; want the auto-generated verify-failure correction prompt", idx, req.Prompt)
			}
			return ports.RunResult{Output: "impl2", Success: true}, nil
		case 4:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"looks good now"}`}, nil
		case 5:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"done"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d: %+v", idx, req)
			return ports.RunResult{}, nil
		}
	}

	cfg := testConfig()
	cfg.LocalVerifyCommand = "go build ./..."
	loop := newTestLoop(agent, store, notifier, pusher, cfg)

	verifyCalls := 0
	loop.Verify = func(ctx context.Context, dir, command string) (string, string, error) {
		verifyCalls++
		if verifyCalls == 1 {
			return "", "# pkg\n./main.go:1:1: syntax error", fmt.Errorf("exit status 1")
		}
		return "ok", "", nil
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if store.runState.Phase != domain.PhaseDone {
		t.Fatalf("got phase %q, want done", store.runState.Phase)
	}
	if verifyCalls != 2 {
		t.Fatalf("got %d verify calls, want 2 (failed retry, then succeeded)", verifyCalls)
	}
	if len(agent.calls) != 6 {
		t.Fatalf("got %d agent calls, want exactly 6 (no extra PO call for the auto-reject)", len(agent.calls))
	}
	if !strings.Contains(agent.calls[3].Prompt, "syntax error") {
		t.Fatalf("retry dev prompt = %q, want it to include the verify failure output", agent.calls[3].Prompt)
	}
	if len(pusher.calls) != 1 {
		t.Fatalf("got %d push calls, want exactly 1 (only after verify passed)", len(pusher.calls))
	}
}

func TestRun_AcceptWithFailingPush_BlocksTask(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}
	pusher := &fakePusher{err: fmt.Errorf("gitcli: git push origin main failed: exit status 1 (stderr: ! [remote rejected] main -> main (protected branch))")}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "impl1", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"ship it"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d: loop should have blocked after the failed push", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, pusher, testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if store.runState.Phase != domain.PhaseBlocked {
		t.Fatalf("got phase %q, want blocked", store.runState.Phase)
	}
	if store.backlog[0].Status != domain.TaskStatusBlocked {
		t.Fatalf("got status %q, want blocked", store.backlog[0].Status)
	}
	if len(pusher.calls) != 1 {
		t.Fatalf("got %d push calls, want exactly 1", len(pusher.calls))
	}
	if len(notifier.messages) == 0 || !strings.Contains(notifier.messages[len(notifier.messages)-1], "protected branch") {
		t.Fatalf("got messages %v, want the last one to include the git push error", notifier.messages)
	}
}

// TestRun_AcceptWithNoVerifyConfigured_StillCallsPush guards against a
// regression where an empty LocalVerifyCommand (verification skipped, as
// documented) accidentally skips the push too. They are independent: empty
// LocalVerifyCommand must still always reach CommitAndPush.
func TestRun_AcceptWithNoVerifyConfigured_StillCallsPush(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}
	pusher := newSuccessPusher()

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "impl1", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"ship it"}`}, nil
		case 3:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"done"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	cfg := testConfig()
	cfg.LocalVerifyCommand = "" // explicitly unset, like the empty-skips-verification doc says
	loop := newTestLoop(agent, store, notifier, pusher, cfg)
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(pusher.calls) != 1 {
		t.Fatalf("got %d push calls, want exactly 1 (empty LocalVerifyCommand must not skip push)", len(pusher.calls))
	}
	if store.runState.Phase != domain.PhaseDone {
		t.Fatalf("got phase %q, want done", store.runState.Phase)
	}
}

// TestRun_VerifyNeverPasses_NeverCallsPushAndEventuallyBlocks is the
// regression test for the task-001 incident: a LocalVerifyCommand that
// always fails (e.g. because the shell it needs isn't on PATH in the
// environment the orchestrator was launched from) must NEVER reach
// CommitAndPush, and must eventually leave the task in `blocked` - not
// silently marked done, and not stuck retrying forever.
func TestRun_VerifyNeverPasses_NeverCallsPushAndEventuallyBlocks(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}
	pusher := newSuccessPusher()

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		default:
			// Every dev call and every PO evaluate call reports "success" -
			// the point is that it never matters, because verify (below)
			// never lets it reach the push step. Distinguish a PO-evaluate
			// call from a Dev call by a string unique to poEvaluatePrompt.
			if strings.Contains(req.Prompt, "Relatório do Desenvolvedor") {
				return ports.RunResult{Output: `{"verdict":"accept","reasoning":"looks fine"}`}, nil
			}
			return ports.RunResult{Output: "impl", Success: true}, nil
		}
	}

	cfg := testConfig()
	cfg.MaxAttemptsPerTask = 2
	cfg.LocalVerifyCommand = "go build ./... && go vet ./... && go test ./..."
	loop := newTestLoop(agent, store, notifier, pusher, cfg)
	loop.Verify = func(ctx context.Context, dir, command string) (string, string, error) {
		return "", "", fmt.Errorf(`exec: "sh": executable file not found in %%PATH%%`)
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(pusher.calls) != 0 {
		t.Fatalf("got %d push calls, want 0 - a permanently failing verify must never reach CommitAndPush", len(pusher.calls))
	}
	if store.runState.Phase != domain.PhaseBlocked {
		t.Fatalf("got phase %q, want blocked (never silently done, never stuck retrying forever)", store.runState.Phase)
	}
	if store.backlog[0].Status != domain.TaskStatusBlocked {
		t.Fatalf("got backlog status %q, want blocked", store.backlog[0].Status)
	}
}

// TestRun_POInventsNewTaskID_AddsTaskToBacklogAsDoneAfterAccept is the
// regression test for the task-004 incident: when the backlog is exhausted
// and the PO invents a task_id that doesn't exist in backlog.json yet,
// markTaskStatus used to silently no-op (task not found -> ignored) instead
// of adding it. The task would get implemented, accepted and pushed, but
// backlog.json would never show any trace of it. It must instead be added
// to the backlog - using the title/description the PO provided in its
// decision - and end up "done" once accepted.
func TestRun_POInventsNewTaskID_AddsTaskToBacklogAsDoneAfterAccept(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{} // backlog exhausted: nothing for the PO to pick from
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"task-099","title":"Invented task","description":"PO invented this because the backlog ran out","dev_prompt":"do it"}`}, nil
		case 1:
			return ports.RunResult{Output: "impl", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"ship it"}`}, nil
		case 3:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"done"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var found *domain.Task
	for i := range store.backlog {
		if store.backlog[i].ID == "task-099" {
			found = &store.backlog[i]
		}
	}
	if found == nil {
		t.Fatalf("got backlog %+v, want it to contain the PO-invented task-099", store.backlog)
	}
	if found.Status != domain.TaskStatusDone {
		t.Fatalf("got task-099 status %q, want done", found.Status)
	}
	if found.Title != "Invented task" || found.Description != "PO invented this because the backlog ran out" {
		t.Fatalf("got task-099 title/description %q/%q, want them preserved from the PO decision", found.Title, found.Description)
	}
}

// ---------------------------------------------------------------------
// real-time progress logging
// ---------------------------------------------------------------------

// TestRun_LogsProgressAtEachTransition verifies that every major state
// transition is logged via the standard log package as it happens - not just
// reflected in the final state/history after the whole cycle completes. This
// is what lets someone watching the terminal see the PO's decision and
// reasoning, the Dev call starting/finishing, and the PO's verdict, in real
// time.
func TestRun_LogsProgressAtEachTransition(t *testing.T) {
	store := newFakeStore(nil)
	store.backlog = []domain.Task{{ID: "t1", Title: "First task", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","title":"First task","dev_prompt":"implement thing","reasoning":"picking it up"}`}, nil
		case 1:
			return ports.RunResult{Output: "done implementing", Success: true, NumTurns: 5, DurationSec: 12.3}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"tests pass"}`}, nil
		case 3:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"nothing left"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d: %+v", idx, req)
			return ports.RunResult{}, nil
		}
	}

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	}()

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := logBuf.String()
	for _, want := range []string{
		"PO analisando backlog",
		"t1",
		"First task",
		"picking it up",
		"Dev iniciando tarefa t1",
		"Dev terminou tarefa t1",
		"success=true",
		"PO avaliando o relatório",
		"veredito=accept",
		"tests pass",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output missing %q\n--- full log ---\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------
// checkpointing
// ---------------------------------------------------------------------

func TestRun_ChecksPointBeforeCallingDevAgent(t *testing.T) {
	var log []string
	store := newFakeStore(&log)
	store.backlog = []domain.Task{{ID: "t1", Status: domain.TaskStatusPending}}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{log: &log}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{Output: `{"action":"next_task","task_id":"t1","dev_prompt":"p1"}`}, nil
		case 1:
			return ports.RunResult{Output: "ok", Success: true}, nil
		case 2:
			return ports.RunResult{Output: `{"verdict":"accept","reasoning":"ok"}`}, nil
		case 3:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"done"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Find "save:dev_running" and confirm it appears before "agent:1" (the
	// dev call) in the combined event log.
	saveIdx, agentIdx := -1, -1
	for i, e := range log {
		if e == "save:dev_running" && saveIdx == -1 {
			saveIdx = i
		}
		if e == "agent:1" && agentIdx == -1 {
			agentIdx = i
		}
	}
	if saveIdx == -1 {
		t.Fatalf("expected a save:dev_running checkpoint in log %v", log)
	}
	if agentIdx == -1 {
		t.Fatalf("expected agent:1 (dev call) in log %v", log)
	}
	if saveIdx > agentIdx {
		t.Fatalf("checkpoint (idx %d) happened after dev call (idx %d), want before", saveIdx, agentIdx)
	}
}

// ---------------------------------------------------------------------
// rate limiting
// ---------------------------------------------------------------------

func TestRun_RateLimited_NotifiesSleepsThenRetriesSameCall(t *testing.T) {
	store := newFakeStore(nil)
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{RateLimited: true}, nil
		case 1:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"x"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	var sleptFor []time.Duration
	loop := newTestLoop(agent, store, notifier, newSuccessPusher(), testConfig())
	loop.Sleep = func(ctx context.Context, d time.Duration) error {
		sleptFor = append(sleptFor, d)
		return nil
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(sleptFor) != 1 || sleptFor[0] != 10*time.Minute {
		t.Fatalf("got sleptFor %v, want a single 10m wait (RateLimitWaitMin)", sleptFor)
	}
	if len(notifier.messages) == 0 || !strings.Contains(strings.ToLower(notifier.messages[0]), "rate limit") {
		t.Fatalf("got messages %v, want a rate limit notification", notifier.messages)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("got %d agent calls, want 2 (rate-limited attempt + retry)", len(agent.calls))
	}
	if store.runState.Phase != domain.PhaseDone {
		t.Fatalf("got phase %q, want done after the retried call succeeded", store.runState.Phase)
	}
}

func TestRun_RateLimitResume_ComputesRemainingWaitFromExistingTimestamp(t *testing.T) {
	fixedNow := time.Unix(1700000000, 0)
	rateLimitedAt := fixedNow.Add(-7 * time.Minute) // "process restarted 7m into the wait"

	store := newFakeStore(nil)
	store.runState = &domain.RunState{
		Phase:         domain.PhasePODeciding,
		RateLimitedAt: &rateLimitedAt,
	}
	notifier := &fakeNotifier{}

	agent := &fakeAgent{}
	agent.respond = func(idx int, req ports.RunRequest) (ports.RunResult, error) {
		switch idx {
		case 0:
			return ports.RunResult{RateLimited: true}, nil
		case 1:
			return ports.RunResult{Output: `{"action":"product_done","reasoning":"x"}`}, nil
		default:
			t.Fatalf("unexpected agent call #%d", idx)
			return ports.RunResult{}, nil
		}
	}

	var sleptFor []time.Duration
	loop := New(agent, store, notifier, newSuccessPusher(), testConfig())
	loop.Now = func() time.Time { return fixedNow }
	loop.Sleep = func(ctx context.Context, d time.Duration) error {
		sleptFor = append(sleptFor, d)
		return nil
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(sleptFor) != 1 || sleptFor[0] != 3*time.Minute {
		t.Fatalf("got sleptFor %v, want a single 3m wait (10m min - 7m already elapsed)", sleptFor)
	}
}

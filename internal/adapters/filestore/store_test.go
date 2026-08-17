package filestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"orchestrator/internal/domain"
)

func TestWriteAtomic_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.md")

	if err := writeAtomic(target, []byte("hello")); err != nil {
		t.Fatalf("writeAtomic returned error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestWriteAtomic_OverwritesExistingContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.md")

	if err := writeAtomic(target, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeAtomic(target, []byte("second")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

func TestWriteAtomic_LeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.md")

	if err := writeAtomic(target, []byte("hello")); err != nil {
		t.Fatalf("writeAtomic returned error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.md" {
		t.Fatalf("expected only state.md in dir, got %v", entries)
	}
}

func TestReadState_ReturnsEmptyWhenFileMissing(t *testing.T) {
	store := New(t.TempDir())

	got, err := store.ReadState(context.Background())
	if err != nil {
		t.Fatalf("ReadState returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

func TestWriteState_ThenReadState_RoundTrips(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	if err := store.WriteState(ctx, "some state"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	got, err := store.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got != "some state" {
		t.Fatalf("got %q, want %q", got, "some state")
	}
}

func TestWriteState_OverwritesRatherThanAppends(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	if err := store.WriteState(ctx, "first version"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if err := store.WriteState(ctx, "second version"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	got, err := store.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got != "second version" {
		t.Fatalf("got %q, want %q (state.md must be overwritten, not appended)", got, "second version")
	}
}

func TestReadBacklog_ReturnsEmptyWhenFileMissing(t *testing.T) {
	store := New(t.TempDir())

	tasks, err := store.ReadBacklog(context.Background())
	if err != nil {
		t.Fatalf("ReadBacklog returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d tasks, want 0", len(tasks))
	}
}

func TestWriteBacklog_ThenReadBacklog_RoundTrips(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	tasks := []domain.Task{
		{ID: "t1", Title: "First task", Status: domain.TaskStatusPending},
		{ID: "t2", Title: "Second task", Status: domain.TaskStatusDone},
	}

	if err := store.WriteBacklog(ctx, tasks); err != nil {
		t.Fatalf("WriteBacklog: %v", err)
	}
	got, err := store.ReadBacklog(ctx)
	if err != nil {
		t.Fatalf("ReadBacklog: %v", err)
	}
	if len(got) != 2 || got[0].ID != "t1" || got[1].Status != domain.TaskStatusDone {
		t.Fatalf("got %+v, want round-tripped tasks", got)
	}
}

func TestLoadRunState_ReturnsIdleWhenFileMissing(t *testing.T) {
	store := New(t.TempDir())

	state, err := store.LoadRunState(context.Background())
	if err != nil {
		t.Fatalf("LoadRunState returned error: %v", err)
	}
	if state.Phase != domain.PhaseIdle {
		t.Fatalf("got phase %q, want %q", state.Phase, domain.PhaseIdle)
	}
}

func TestSaveRunState_ThenLoadRunState_RoundTrips(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	state := &domain.RunState{
		Phase:          domain.PhaseDevPending,
		CurrentTaskID:  "t1",
		CycleCount:     3,
		AttemptsOnTask: 1,
	}

	if err := store.SaveRunState(ctx, state); err != nil {
		t.Fatalf("SaveRunState: %v", err)
	}
	got, err := store.LoadRunState(ctx)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if got.Phase != domain.PhaseDevPending || got.CurrentTaskID != "t1" || got.CycleCount != 3 {
		t.Fatalf("got %+v, want round-tripped state", got)
	}
}

func TestAppendHistory_AppendsAcrossMultipleCalls(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	if err := store.AppendHistory(ctx, "t1", "entry one\n"); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := store.AppendHistory(ctx, "t1", "entry two\n"); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(store.Root, "history", "t1.md"))
	if err != nil {
		t.Fatalf("reading history file: %v", err)
	}
	want := "entry one\nentry two\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReadVision_ReturnsEmptyWhenFileMissing(t *testing.T) {
	store := New(t.TempDir())

	got, err := store.ReadVision(context.Background())
	if err != nil {
		t.Fatalf("ReadVision returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

func TestReadVision_ReturnsFileContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vision.md"), []byte("# Vision"), 0o644); err != nil {
		t.Fatalf("seeding vision.md: %v", err)
	}
	store := New(dir)

	got, err := store.ReadVision(context.Background())
	if err != nil {
		t.Fatalf("ReadVision: %v", err)
	}
	if got != "# Vision" {
		t.Fatalf("got %q, want %q", got, "# Vision")
	}
}

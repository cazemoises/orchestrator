// Package filestore implements ports.MemoryStore by reading and writing
// plain files under a configurable root directory.
package filestore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"orchestrator/internal/domain"
	"orchestrator/internal/ports"
)

var _ ports.MemoryStore = (*FileStore)(nil)

// FileStore persists orchestrator memory as files under Root:
//
//	Root/vision.md
//	Root/state.md
//	Root/backlog.json
//	Root/run_state.json
//	Root/history/<taskID>.md
type FileStore struct {
	Root string
}

// New creates a FileStore rooted at root. root does not need to exist yet.
func New(root string) *FileStore {
	return &FileStore{Root: root}
}

// writeAtomic writes data to path without ever leaving a truncated file in
// place if the process dies mid-write: it writes to a sibling .tmp file and
// renames it over the target, which is atomic on both POSIX and Windows.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readFileOrEmpty returns the file contents, or "" if the file does not
// exist yet (the natural state before the first cycle has run).
func readFileOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *FileStore) visionPath() string   { return filepath.Join(s.Root, "vision.md") }
func (s *FileStore) statePath() string    { return filepath.Join(s.Root, "state.md") }
func (s *FileStore) backlogPath() string  { return filepath.Join(s.Root, "backlog.json") }
func (s *FileStore) runStatePath() string { return filepath.Join(s.Root, "run_state.json") }
func (s *FileStore) historyPath(taskID string) string {
	return filepath.Join(s.Root, "history", taskID+".md")
}

func (s *FileStore) ReadVision(ctx context.Context) (string, error) {
	return readFileOrEmpty(s.visionPath())
}

func (s *FileStore) ReadState(ctx context.Context) (string, error) {
	return readFileOrEmpty(s.statePath())
}

// WriteState overwrites state.md with content. It is intentionally not an
// append: state.md always holds the current complete picture so it does not
// grow without bound across cycles.
func (s *FileStore) WriteState(ctx context.Context, content string) error {
	return writeAtomic(s.statePath(), []byte(content))
}

func (s *FileStore) ReadBacklog(ctx context.Context) ([]domain.Task, error) {
	data, err := os.ReadFile(s.backlogPath())
	if os.IsNotExist(err) {
		return []domain.Task{}, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []domain.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *FileStore) WriteBacklog(ctx context.Context, tasks []domain.Task) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.backlogPath(), data)
}

// AppendHistory appends content to history/<taskID>.md, creating the file
// (and the history/ directory) on first use. Unlike state.md, history is
// meant to grow: it is the audit trail of everything that happened on a
// task.
func (s *FileStore) AppendHistory(ctx context.Context, taskID string, content string) error {
	dir := filepath.Join(s.Root, "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.historyPath(taskID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func (s *FileStore) LoadRunState(ctx context.Context) (*domain.RunState, error) {
	data, err := os.ReadFile(s.runStatePath())
	if os.IsNotExist(err) {
		return &domain.RunState{Phase: domain.PhaseIdle}, nil
	}
	if err != nil {
		return nil, err
	}
	var state domain.RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *FileStore) SaveRunState(ctx context.Context, state *domain.RunState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.runStatePath(), data)
}

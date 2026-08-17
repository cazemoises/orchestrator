package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orchestrator/internal/ports"
)

// runGitT is a small test helper wrapping exec.Command against git,
// failing the test immediately on error.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newTestRepoWithRemote sets up a bare "remote" repo and a working repo
// with origin pointing at it, an initial commit already pushed to main.
func newTestRepoWithRemote(t *testing.T) (repoDir, remoteDir string) {
	t.Helper()

	remoteDir = t.TempDir()
	runGitT(t, remoteDir, "init", "--bare")

	repoDir = t.TempDir()
	runGitT(t, repoDir, "init", "-b", "main")
	runGitT(t, repoDir, "config", "user.email", "test@example.com")
	runGitT(t, repoDir, "config", "user.name", "Test")
	runGitT(t, repoDir, "remote", "add", "origin", remoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("writing initial file: %v", err)
	}
	runGitT(t, repoDir, "add", "-A")
	runGitT(t, repoDir, "commit", "-m", "initial commit")
	runGitT(t, repoDir, "push", "origin", "main")

	return repoDir, remoteDir
}

func remoteHead(t *testing.T, remoteDir string) string {
	t.Helper()
	return strings.TrimSpace(runGitT(t, remoteDir, "rev-parse", "refs/heads/main"))
}

func TestCommitAndPush_PushesWhenThereAreChanges(t *testing.T) {
	repoDir, remoteDir := newTestRepoWithRemote(t)
	before := remoteHead(t, remoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("writing new file: %v", err)
	}

	p := &Pusher{}
	res, err := p.CommitAndPush(context.Background(), ports.PushRequest{
		RepoDir:       repoDir,
		CommitMessage: "feat: add new.txt",
	})
	if err != nil {
		t.Fatalf("CommitAndPush returned error: %v", err)
	}
	if !res.Pushed || res.Skipped {
		t.Fatalf("got %+v, want Pushed=true Skipped=false", res)
	}
	if res.CommitHash == "" {
		t.Fatal("expected a non-empty CommitHash")
	}

	after := remoteHead(t, remoteDir)
	if after == before {
		t.Fatal("expected remote HEAD to move, it didn't")
	}
	if after != res.CommitHash {
		t.Fatalf("remote HEAD = %q, want it to match returned CommitHash %q", after, res.CommitHash)
	}
}

func TestCommitAndPush_SkipsWhenNoChanges(t *testing.T) {
	repoDir, remoteDir := newTestRepoWithRemote(t)
	before := remoteHead(t, remoteDir)

	p := &Pusher{}
	res, err := p.CommitAndPush(context.Background(), ports.PushRequest{
		RepoDir:       repoDir,
		CommitMessage: "feat: nothing changed",
	})
	if err != nil {
		t.Fatalf("CommitAndPush returned error: %v", err)
	}
	if !res.Skipped || res.Pushed {
		t.Fatalf("got %+v, want Skipped=true Pushed=false", res)
	}

	after := remoteHead(t, remoteDir)
	if after != before {
		t.Fatal("expected remote HEAD to stay the same when there was nothing to commit")
	}
}

func TestCommitAndPush_ReturnsErrorWhenPushFails(t *testing.T) {
	repoDir := t.TempDir()
	runGitT(t, repoDir, "init", "-b", "main")
	runGitT(t, repoDir, "config", "user.email", "test@example.com")
	runGitT(t, repoDir, "config", "user.name", "Test")

	// Point origin at a path that is not a git repo at all, so the push
	// step fails deterministically.
	badRemote := filepath.Join(t.TempDir(), "does-not-exist")
	runGitT(t, repoDir, "remote", "add", "origin", badRemote)

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	p := &Pusher{}
	_, err := p.CommitAndPush(context.Background(), ports.PushRequest{
		RepoDir:       repoDir,
		CommitMessage: "feat: this will fail to push",
	})
	if err == nil {
		t.Fatal("expected an error when push fails, got nil")
	}
}

var _ ports.GitPusher = (*Pusher)(nil)

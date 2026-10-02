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
	if !res.Pushed || !res.CommitCreated {
		t.Fatalf("got %+v, want Pushed=true CommitCreated=true", res)
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

func TestCommitAndPush_SkipsWhenNoChangesAndNothingAheadOfRemote(t *testing.T) {
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
	if res.CommitCreated || res.Pushed {
		t.Fatalf("got %+v, want CommitCreated=false Pushed=false", res)
	}

	after := remoteHead(t, remoteDir)
	if after != before {
		t.Fatal("expected remote HEAD to stay the same when there was nothing to commit or push")
	}
}

// TestCommitAndPush_PushesPreExistingCommitsWhenWorkingTreeCleanButAheadOfRemote
// reproduces the real bug: the Dev agent commits its own work directly via
// Bash (normal practice - its prompt asks for atomic commits), leaving the
// working tree clean by the time CommitAndPush runs. A clean working tree
// alone must not be read as "nothing to push" - HEAD can still be ahead of
// the remote-tracking branch, and those commits must go out too.
func TestCommitAndPush_PushesPreExistingCommitsWhenWorkingTreeCleanButAheadOfRemote(t *testing.T) {
	repoDir, remoteDir := newTestRepoWithRemote(t)
	before := remoteHead(t, remoteDir)

	// Simulate the Dev agent committing its own work directly, out-of-band
	// from CommitAndPush, and never pushing it itself.
	if err := os.WriteFile(filepath.Join(repoDir, "dev-work.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("writing dev-work.txt: %v", err)
	}
	runGitT(t, repoDir, "add", "-A")
	runGitT(t, repoDir, "commit", "-m", "dev: implement the task")
	devCommitHash := strings.TrimSpace(runGitT(t, repoDir, "rev-parse", "HEAD"))

	p := &Pusher{}
	res, err := p.CommitAndPush(context.Background(), ports.PushRequest{
		RepoDir:       repoDir,
		CommitMessage: "feat: nothing changed",
	})
	if err != nil {
		t.Fatalf("CommitAndPush returned error: %v", err)
	}
	if !res.Pushed || res.CommitCreated {
		t.Fatalf("got %+v, want Pushed=true CommitCreated=false", res)
	}
	if res.CommitHash != devCommitHash {
		t.Fatalf("got CommitHash %q, want the Dev agent's own commit %q", res.CommitHash, devCommitHash)
	}

	after := remoteHead(t, remoteDir)
	if after == before {
		t.Fatal("expected remote HEAD to move to the Dev agent's pre-existing commit, it didn't")
	}
	if after != devCommitHash {
		t.Fatalf("remote HEAD = %q, want it to match the Dev agent's commit %q", after, devCommitHash)
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

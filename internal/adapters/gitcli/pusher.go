// Package gitcli implements ports.GitPusher by shelling out to the `git`
// binary already configured on the machine (credentials/SSH keys are
// assumed to already work for `git push` run by hand - this package does
// not manage authentication).
package gitcli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"orchestrator/internal/ports"
)

var _ ports.GitPusher = (*Pusher)(nil)

// Pusher is the production ports.GitPusher backed by the git CLI.
type Pusher struct {
	// GitPath overrides the git binary to invoke; defaults to "git" on PATH.
	GitPath string
}

func (p *Pusher) gitPath() string {
	if p.GitPath != "" {
		return p.GitPath
	}
	return "git"
}

func (p *Pusher) run(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, p.gitPath(), args...)
	cmd.Dir = dir

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func (p *Pusher) CommitAndPush(ctx context.Context, req ports.PushRequest) (ports.PushResult, error) {
	if _, stderr, err := p.run(ctx, req.RepoDir, "add", "-A"); err != nil {
		return ports.PushResult{}, fmt.Errorf("gitcli: git add -A failed: %w (stderr: %s)", err, stderr)
	}

	statusOut, stderr, err := p.run(ctx, req.RepoDir, "status", "--porcelain")
	if err != nil {
		return ports.PushResult{}, fmt.Errorf("gitcli: git status --porcelain failed: %w (stderr: %s)", err, stderr)
	}
	if strings.TrimSpace(statusOut) == "" {
		return ports.PushResult{Skipped: true}, nil
	}

	if _, stderr, err := p.run(ctx, req.RepoDir, "commit", "-m", req.CommitMessage); err != nil {
		return ports.PushResult{}, fmt.Errorf("gitcli: git commit failed: %w (stderr: %s)", err, stderr)
	}

	hashOut, stderr, err := p.run(ctx, req.RepoDir, "rev-parse", "HEAD")
	if err != nil {
		return ports.PushResult{}, fmt.Errorf("gitcli: git rev-parse HEAD failed: %w (stderr: %s)", err, stderr)
	}
	hash := strings.TrimSpace(hashOut)

	branchOut, stderr, err := p.run(ctx, req.RepoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ports.PushResult{CommitHash: hash}, fmt.Errorf("gitcli: git rev-parse --abbrev-ref HEAD failed: %w (stderr: %s)", err, stderr)
	}
	branch := strings.TrimSpace(branchOut)

	if _, stderr, err := p.run(ctx, req.RepoDir, "push", "origin", branch); err != nil {
		return ports.PushResult{CommitHash: hash}, fmt.Errorf("gitcli: git push origin %s failed: %w (stderr: %s)", branch, err, stderr)
	}

	return ports.PushResult{Pushed: true, CommitHash: hash}, nil
}

// Command orchestrator runs the autonomous PO/Dev loop against a target
// repo, driven entirely by the Claude Code CLI (`claude -p`, authenticated
// via CLAUDE_CODE_OAUTH_TOKEN). See README.md for setup.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"orchestrator/internal/adapters/claudecli"
	"orchestrator/internal/adapters/filestore"
	"orchestrator/internal/adapters/gitcli"
	"orchestrator/internal/adapters/notifier"
	"orchestrator/internal/orchestrator"
	"orchestrator/internal/ports"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repoDir := envOr("ORCH_REPO_DIR", ".")
	dataDir := envOr("ORCH_DATA_DIR", "./data")

	cfg := orchestrator.Config{
		RepoDir:            repoDir,
		MaxAttemptsPerTask: envInt("ORCH_MAX_ATTEMPTS_PER_TASK", 0),
		MaxTasksPerCycle:   envInt("ORCH_MAX_TASKS_PER_CYCLE", 0),
		DevAllowedTools:    envList("ORCH_DEV_ALLOWED_TOOLS"),
		POModel:            os.Getenv("ORCH_PO_MODEL"),
		DevModel:           os.Getenv("ORCH_DEV_MODEL"),
		LocalVerifyCommand: os.Getenv("ORCH_LOCAL_VERIFY_COMMAND"),
		GitCommitPrefix:    os.Getenv("ORCH_GIT_COMMIT_PREFIX"),
	}

	store := filestore.New(dataDir)
	agent := claudecli.NewRunner()
	pusher := &gitcli.Pusher{}

	var notif ports.Notifier
	if webhook := os.Getenv("ORCH_NOTIFY_WEBHOOK"); webhook != "" {
		notif = notifier.NewWebhook(webhook)
	} else {
		notif = notifier.NewStdout()
	}

	loop := orchestrator.New(agent, store, notif, pusher, cfg)

	log.Printf("orchestrator: starting (repo_dir=%s data_dir=%s)", repoDir, dataDir)
	err := loop.Run(ctx)
	if err != nil && ctx.Err() == nil {
		log.Fatalf("orchestrator: exited with error: %v", err)
	}
	if ctx.Err() != nil {
		log.Println("orchestrator: received shutdown signal, state checkpointed, exiting cleanly")
		return
	}
	log.Println("orchestrator: run finished, state checkpointed")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("orchestrator: ignoring invalid %s=%q (%v), using default %d", key, v, err, fallback)
		return fallback
	}
	return n
}

func envList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

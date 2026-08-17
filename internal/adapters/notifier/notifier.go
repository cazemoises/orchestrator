// Package notifier implements ports.Notifier: ways to surface a message out
// of the loop, from a plain log line to an outbound webhook.
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"orchestrator/internal/ports"
)

var (
	_ ports.Notifier = (*StdoutNotifier)(nil)
	_ ports.Notifier = (*WebhookNotifier)(nil)
)

// StdoutNotifier logs messages to Out (os.Stdout by default).
type StdoutNotifier struct {
	Out io.Writer
}

// NewStdout creates a StdoutNotifier writing to os.Stdout.
func NewStdout() *StdoutNotifier {
	return &StdoutNotifier{Out: os.Stdout}
}

func (n *StdoutNotifier) Notify(ctx context.Context, message string) error {
	out := n.Out
	if out == nil {
		out = os.Stdout
	}
	_, err := fmt.Fprintf(out, "[%s] %s\n", time.Now().Format(time.RFC3339), message)
	return err
}

// WebhookNotifier POSTs {"message": ...} as JSON to URL.
type WebhookNotifier struct {
	URL    string
	Client *http.Client
}

// NewWebhook creates a WebhookNotifier posting to url with a default timeout.
func NewWebhook(url string) *WebhookNotifier {
	return &WebhookNotifier{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (n *WebhookNotifier) Notify(ctx context.Context, message string) error {
	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}

	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifier: webhook returned status %d", resp.StatusCode)
	}
	return nil
}

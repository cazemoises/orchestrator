package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStdoutNotifier_WritesMessageToWriter(t *testing.T) {
	var buf strings.Builder
	n := &StdoutNotifier{Out: &buf}

	if err := n.Notify(context.Background(), "hello world"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("expected output to contain message, got %q", buf.String())
	}
}

func TestWebhookNotifier_PostsJSONMessageToURL(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL, Client: srv.Client()}

	if err := n.Notify(context.Background(), "task blocked"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("got method %q, want POST", gotMethod)
	}
	if !strings.Contains(gotContentType, "application/json") {
		t.Fatalf("got content-type %q, want application/json", gotContentType)
	}
	if gotBody["message"] != "task blocked" {
		t.Fatalf("got body %+v, want message=%q", gotBody, "task blocked")
	}
}

func TestWebhookNotifier_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL, Client: srv.Client()}

	if err := n.Notify(context.Background(), "hello"); err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
}

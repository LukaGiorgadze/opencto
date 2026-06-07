package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencto/opencto/internal/agent"
)

func TestBifrostHTTPClientAddsWorkflowRunHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(bifrostVirtualKeyHeader) != "test-key" {
			t.Fatalf("expected bifrost virtual key header, got %q", r.Header.Get(bifrostVirtualKeyHeader))
		}
		if r.Header.Get(bifrostSessionIDHeader) != "run-1" {
			t.Fatalf("expected session header, got %q", r.Header.Get(bifrostSessionIDHeader))
		}
		if r.Header.Get("x-bf-dim-project-id") != "project-1" {
			t.Fatalf("expected project dimension header, got %q", r.Header.Get("x-bf-dim-project-id"))
		}
		if r.Header.Get("x-bf-dim-workflow-id") != "workflow-1" {
			t.Fatalf("expected workflow dimension header, got %q", r.Header.Get("x-bf-dim-workflow-id"))
		}
		if r.Header.Get("x-bf-dim-workflow-run-id") != "run-1" {
			t.Fatalf("expected workflow run dimension header, got %q", r.Header.Get("x-bf-dim-workflow-run-id"))
		}
		if r.Header.Get("x-bf-dim-request-kind") != "next_action" {
			t.Fatalf("expected request kind dimension header, got %q", r.Header.Get("x-bf-dim-request-kind"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer server.Close()

	ctx := agent.ContextWithLLMSession(context.Background(), agent.LLMSession{
		ProjectID:     "project-1",
		WorkflowID:    "workflow-1",
		WorkflowRunID: "run-1",
		RequestKind:   "next_action",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	resp, err := newOpenAIHTTPClient(true).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
}

func TestBifrostHTTPClientAddsVirtualKeyWithoutWorkflowRun(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(bifrostVirtualKeyHeader) != "test-key" {
			t.Fatalf("expected bifrost virtual key header, got %q", r.Header.Get(bifrostVirtualKeyHeader))
		}
		if r.Header.Get(bifrostSessionIDHeader) != "" {
			t.Fatalf("expected no session header, got %q", r.Header.Get(bifrostSessionIDHeader))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	resp, err := NewOpenAIHTTPClient(true).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
}

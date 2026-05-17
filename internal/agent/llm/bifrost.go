package llm

import (
	"net/http"
	"time"

	"github.com/opencto/opencto/internal/agent"
)

const (
	bifrostSessionIDHeader = "x-bf-session-id"
	bifrostDimensionPrefix = "x-bf-dim-"
)

type bifrostRoundTripper struct {
	enabled bool
	base    http.RoundTripper
}

func newOpenAIHTTPClient(bifrostEnabled bool) *http.Client {
	return &http.Client{
		Timeout:   2 * time.Minute,
		Transport: bifrostRoundTripper{enabled: bifrostEnabled, base: http.DefaultTransport},
	}
}

func (t bifrostRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if !t.enabled {
		return base.RoundTrip(req)
	}

	session := agent.LLMSessionFromContext(req.Context())
	if session.WorkflowRunID == "" {
		return base.RoundTrip(req)
	}

	req = req.Clone(req.Context())
	req.Header.Set(bifrostSessionIDHeader, session.WorkflowRunID)
	setBifrostDimension(req, "project-id", session.ProjectID)
	setBifrostDimension(req, "workflow-id", session.WorkflowID)
	setBifrostDimension(req, "workflow-run-id", session.WorkflowRunID)
	setBifrostDimension(req, "request-kind", session.RequestKind)
	return base.RoundTrip(req)
}

func setBifrostDimension(req *http.Request, name string, value string) {
	if value == "" {
		return
	}
	req.Header.Set(bifrostDimensionPrefix+name, value)
}

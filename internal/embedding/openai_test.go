package embedding

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestOpenAIEmbedderPostsEmbeddingRequest(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "text-embedding-3-small" || request.Dimensions != 1536 {
			t.Fatalf("unexpected embedding request: %#v", request)
		}
		if len(request.Input) != 1 || request.Input[0] != "content: Use SQLite." {
			t.Fatalf("unexpected embedding inputs: %#v", request.Input)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(bytes.NewBufferString(`{"model":"text-embedding-3-small","data":[{"index":0,"embedding":[1,0,0]}]}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}

	embedder, err := NewOpenAIEmbedder(OpenAIConfig{
		APIKey:     "test-key",
		BaseURL:    "https://example.test",
		Model:      "text-embedding-3-small",
		Dimensions: 1536,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	result, err := embedder.Embed(t.Context(), []string{"content: Use SQLite."})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if result.Model != "text-embedding-3-small" || result.Dimensions != 1536 {
		t.Fatalf("unexpected embedding result metadata: %#v", result)
	}
	if len(result.Embeddings) != 1 || len(result.Embeddings[0]) != 3 || result.Embeddings[0][0] != 1 {
		t.Fatalf("unexpected embedding result: %#v", result.Embeddings)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

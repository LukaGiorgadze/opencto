package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type OpenAIConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	Dimensions int
	HTTPClient *http.Client
}

type OpenAIEmbedder struct {
	apiKey     string
	baseURL    string
	model      string
	dimensions int
	client     *http.Client
}

func NewOpenAIEmbedder(cfg OpenAIConfig) (*OpenAIEmbedder, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("openai embedding api key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultOpenAIModel
	}
	dimensions := cfg.Dimensions
	if dimensions == 0 {
		dimensions = DefaultDimensions
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &OpenAIEmbedder{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		model:      model,
		dimensions: dimensions,
		client:     client,
	}, nil
}

func (e *OpenAIEmbedder) Provider() string {
	return ProviderOpenAI
}

func (e *OpenAIEmbedder) Model() string {
	return e.model
}

func (e *OpenAIEmbedder) Dimensions() int {
	return e.dimensions
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) (Result, error) {
	cleaned := make([]string, 0, len(inputs))
	indexes := make([]int, 0, len(inputs))
	for i, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		cleaned = append(cleaned, input)
		indexes = append(indexes, i)
	}
	if len(cleaned) == 0 {
		return Result{}, fmt.Errorf("embedding input is required")
	}

	body := map[string]any{
		"model": e.model,
		"input": cleaned,
	}
	if e.dimensions > 0 {
		body["dimensions"] = e.dimensions
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.embeddingsURL(), bytes.NewReader(data))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("embedding request failed with status %s: %s", resp.Status, strings.TrimSpace(string(respData)))
	}

	var output struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respData, &output); err != nil {
		return Result{}, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(output.Data) != len(cleaned) {
		return Result{}, fmt.Errorf("embedding response count mismatch: got %d, want %d", len(output.Data), len(cleaned))
	}
	sort.Slice(output.Data, func(i, j int) bool {
		return output.Data[i].Index < output.Data[j].Index
	})

	embeddings := make([][]float32, len(inputs))
	for _, item := range output.Data {
		if item.Index < 0 || item.Index >= len(cleaned) {
			return Result{}, fmt.Errorf("embedding response index out of range: %d", item.Index)
		}
		vector := make([]float32, len(item.Embedding))
		for i, value := range item.Embedding {
			vector[i] = float32(value)
		}
		embeddings[indexes[item.Index]] = vector
	}
	return Result{
		Embeddings: embeddings,
		Model:      firstNonEmpty(output.Model, e.model),
		Dimensions: e.dimensions,
	}, nil
}

func (e *OpenAIEmbedder) embeddingsURL() string {
	if e.baseURL == "" {
		return "https://api.openai.com/v1/embeddings"
	}
	return e.baseURL + "/embeddings"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

package postprocess

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

type Request struct {
	ProjectID        string
	WorkItemID       string
	ToolCallID       string
	WorkspaceRoot    string
	Tool             domain.ToolType
	Status           domain.ExecutionStatus
	Input            json.RawMessage
	Error            string
	ResultCode       string
	WorkingDirectory string
}

type Result struct {
	Observation string
	Metadata    map[string]string
}

type Processor interface {
	ProcessToolResult(context.Context, Request, Result) Result
}

type ProcessorFunc func(context.Context, Request, Result) Result

func (f ProcessorFunc) ProcessToolResult(ctx context.Context, req Request, result Result) Result {
	return f(ctx, req, result)
}

func Apply(ctx context.Context, processors []Processor, req Request, result Result) Result {
	result.Metadata = CloneMetadata(result.Metadata)
	for _, processor := range processors {
		if processor == nil {
			continue
		}
		result = processor.ProcessToolResult(ctx, req, result)
	}
	if len(result.Metadata) == 0 {
		result.Metadata = nil
	}
	return result
}

func CloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func EnsureMetadata(metadata map[string]string) map[string]string {
	if metadata != nil {
		return metadata
	}
	return map[string]string{}
}

func AppendObservationNote(observation, note string) string {
	observation = strings.TrimSpace(observation)
	note = strings.TrimSpace(note)
	if note == "" {
		return observation
	}
	if observation == "" {
		return note
	}
	return observation + "\n\n" + note
}

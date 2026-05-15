package postprocess

import (
	"context"
	"testing"
)

func TestApplyRunsProcessorsInOrder(t *testing.T) {
	t.Parallel()

	processors := []Processor{
		ProcessorFunc(func(_ context.Context, _ Request, result Result) Result {
			result.Metadata = EnsureMetadata(result.Metadata)
			result.Metadata["first"] = "true"
			result.Observation = AppendObservationNote(result.Observation, "first")
			return result
		}),
		ProcessorFunc(func(_ context.Context, _ Request, result Result) Result {
			if result.Metadata["first"] == "true" {
				result.Metadata["second"] = "true"
			}
			result.Observation = AppendObservationNote(result.Observation, "second")
			return result
		}),
	}

	result := Apply(context.Background(), processors, Request{}, Result{Observation: "base"})
	if result.Observation != "base\n\nfirst\n\nsecond" {
		t.Fatalf("unexpected observation: %q", result.Observation)
	}
	if result.Metadata["first"] != "true" || result.Metadata["second"] != "true" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
}

func TestApplyClonesMetadataBeforeProcessing(t *testing.T) {
	t.Parallel()

	metadata := map[string]string{"original": "true"}
	result := Apply(context.Background(), []Processor{
		ProcessorFunc(func(_ context.Context, _ Request, result Result) Result {
			result.Metadata["added"] = "true"
			return result
		}),
	}, Request{}, Result{Metadata: metadata})

	if result.Metadata["added"] != "true" {
		t.Fatalf("expected added metadata, got %#v", result.Metadata)
	}
	if _, ok := metadata["added"]; ok {
		t.Fatalf("processor mutated original metadata: %#v", metadata)
	}
}

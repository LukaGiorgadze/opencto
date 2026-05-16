package activities

import (
	"context"
	"fmt"
	"strings"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/storage"
)

func (a *Activities) CompressAgentObservations(ctx context.Context, request CompressAgentObservationsRequest) (CompressAgentObservationsResult, error) {
	observations := append([]agent.ExecutionFeedback(nil), request.Observations...)
	result := CompressAgentObservationsResult{
		Summary:               strings.TrimSpace(request.PreviousSummary),
		RemainingObservations: observations,
		MessageCount:          len(observations),
		SourceChars:           agentObservationSourceChars(observations) + len(strings.TrimSpace(request.PreviousSummary)),
	}
	if len(observations) == 0 {
		return result, nil
	}
	recent := storage.DefaultConversationSummaryRecentMessages(a.ConversationSummaryRecent)
	if recent < 1 {
		recent = 1
	}
	if len(observations) <= recent {
		return result, nil
	}
	trigger := storage.DefaultConversationSummaryTriggerChars(a.ConversationSummaryTrigger)
	if result.SourceChars < trigger {
		return result, nil
	}

	candidates := observations[:len(observations)-recent]
	remaining := append([]agent.ExecutionFeedback(nil), observations[len(observations)-recent:]...)
	candidateChars := agentObservationSourceChars(candidates) + len(strings.TrimSpace(request.PreviousSummary))
	maxContextChars := storage.DefaultConversationMaxContextChars(a.ConversationMaxContextChars)
	if a.AgentObservationCompressor == nil {
		result.CompressionUnavailable = true
		if candidateChars > maxContextChars {
			return result, fmt.Errorf("agent observation compressor is not configured and raw agent history exceeds context budget")
		}
		return result, nil
	}

	output, err := a.AgentObservationCompressor.CompressAgentObservations(ctx, agent.AgentObservationCompressionInput{
		ProjectID:       strings.TrimSpace(request.ProjectID),
		Goal:            strings.TrimSpace(request.Goal),
		PreviousSummary: strings.TrimSpace(request.PreviousSummary),
		Observations:    candidates,
		MaxSummaryChars: storage.DefaultConversationSummaryMaxChars(a.ConversationSummaryMaxChars),
	})
	if err != nil {
		if candidateChars > maxContextChars {
			return result, fmt.Errorf("compress agent observations: %w", err)
		}
		return result, nil
	}
	summary := strings.TrimSpace(output.Summary)
	if summary == "" {
		if candidateChars > maxContextChars {
			return result, fmt.Errorf("agent observation compressor returned an empty summary and raw agent history exceeds context budget")
		}
		return result, nil
	}
	result.Summarized = true
	result.Summary = summary
	result.RemainingObservations = remaining
	result.MessageCount = len(candidates)
	result.SourceChars = candidateChars
	return result, nil
}

func agentObservationSourceChars(observations []agent.ExecutionFeedback) int {
	total := 0
	for _, observation := range observations {
		total += len(strings.TrimSpace(string(observation.Tool)))
		total += len(strings.TrimSpace(observation.Status))
		total += len(strings.TrimSpace(observation.RequestedAction))
		total += len(strings.TrimSpace(observation.Command))
		for _, arg := range observation.Args {
			total += len(strings.TrimSpace(arg))
		}
		total += len(strings.TrimSpace(string(observation.Input)))
		total += len(strings.TrimSpace(observation.Observation))
		total += len(strings.TrimSpace(observation.Error))
		for key, value := range observation.Metadata {
			total += len(strings.TrimSpace(key))
			total += len(strings.TrimSpace(value))
		}
	}
	return total
}

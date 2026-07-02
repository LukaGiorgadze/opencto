package activities

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

const (
	onboardingSourceAutomatic = "automatic"
	onboardingSourceCommand   = "command"
	onboardingSourceAnswer    = "answer"
	agentMailAPIKeyEnv        = "AGENTMAIL_API_KEY"
)

func (a *Activities) PrepareOnboarding(ctx context.Context, request PrepareOnboardingRequest) (PrepareOnboardingResult, error) {
	projectID := strings.TrimSpace(firstNonEmpty(request.ProjectID, request.Event.ProjectID, a.Project.ID))
	if projectID == "" {
		return PrepareOnboardingResult{}, nil
	}
	if scheduled.IsScheduledTaskEvent(request.Event) {
		return PrepareOnboardingResult{}, nil
	}

	explicit := domain.IsOnboardingCommand(request.Event.Body)
	if a.Store == nil {
		if explicit {
			return PrepareOnboardingResult{Onboarding: activeOnboarding(onboardingSourceCommand, "")}, nil
		}
		return PrepareOnboardingResult{}, nil
	}

	state, ok, err := a.Store.GetOnboardingState(ctx, projectID)
	if err != nil {
		return PrepareOnboardingResult{}, err
	}
	if explicit {
		if err := a.markOnboarding(ctx, projectID, domain.OnboardingStatusPrompted, onboardingSourceCommand, request.Event); err != nil {
			return PrepareOnboardingResult{}, err
		}
		a.logActivityStep(
			"Onboarding", "command",
			slog.String("project_id", projectID),
			slog.String("event_id", request.Event.ID),
			slog.String("previous_status", string(state.Status)),
		)
		return PrepareOnboardingResult{Onboarding: activeOnboarding(onboardingSourceCommand, string(domain.OnboardingStatusPrompted))}, nil
	}

	if ok && onboardingStatusTerminal(state.Status) {
		a.logActivityStep(
			"Onboarding", "skip_existing_status",
			slog.String("project_id", projectID),
			slog.String("event_id", request.Event.ID),
			slog.String("status", string(state.Status)),
		)
		return PrepareOnboardingResult{}, nil
	}
	if ok && state.Status == domain.OnboardingStatusPrompted {
		a.logActivityStep(
			"Onboarding", "answer",
			slog.String("project_id", projectID),
			slog.String("event_id", request.Event.ID),
			slog.String("status", string(state.Status)),
		)
		return PrepareOnboardingResult{Onboarding: activeOnboarding(onboardingSourceAnswer, string(state.Status))}, nil
	}

	if err := a.markOnboarding(ctx, projectID, domain.OnboardingStatusPrompted, onboardingSourceAutomatic, request.Event); err != nil {
		return PrepareOnboardingResult{}, err
	}
	a.logActivityStep(
		"Onboarding", "automatic",
		slog.String("project_id", projectID),
		slog.String("event_id", request.Event.ID),
	)
	return PrepareOnboardingResult{Onboarding: activeOnboarding(onboardingSourceAutomatic, string(domain.OnboardingStatusPrompted))}, nil
}

func (a *Activities) FinalizeOnboarding(ctx context.Context, request FinalizeOnboardingRequest) error {
	if !request.Onboarding.Active || a.Store == nil {
		return nil
	}
	source := strings.TrimSpace(request.Onboarding.Source)
	if source != onboardingSourceAnswer && !explicitOnboardingWithInlineResponse(request.Event, source) {
		return nil
	}
	projectID := strings.TrimSpace(firstNonEmpty(request.ProjectID, request.Event.ProjectID, a.Project.ID))
	if projectID == "" {
		return nil
	}
	state, ok, err := a.Store.GetOnboardingState(ctx, projectID)
	if err != nil {
		return err
	}
	if !ok || state.Status != domain.OnboardingStatusPrompted {
		return nil
	}
	if err := a.markOnboarding(ctx, projectID, domain.OnboardingStatusSkipped, source, request.Event); err != nil {
		return err
	}
	a.logActivityStep(
		"Onboarding", "skipped",
		slog.String("project_id", projectID),
		slog.String("event_id", request.Event.ID),
		slog.String("source", source),
	)
	return nil
}

func onboardingStatusTerminal(status domain.OnboardingStatus) bool {
	return status == domain.OnboardingStatusCompleted || status == domain.OnboardingStatusSkipped
}

func explicitOnboardingWithInlineResponse(event domain.Event, source string) bool {
	if source != onboardingSourceCommand {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(event.Body))
	return len(fields) > 1 && domain.IsOnboardingCommand(fields[0])
}

func activeOnboarding(source, status string) agent.OnboardingContext {
	return agent.OnboardingContext{
		Active: true,
		Source: strings.TrimSpace(source),
		Status: strings.TrimSpace(status),
	}
}

func agentMailAPIKeyAvailable() bool {
	return strings.TrimSpace(os.Getenv(agentMailAPIKeyEnv)) != ""
}

func (a *Activities) markOnboarding(ctx context.Context, projectID string, status domain.OnboardingStatus, source string, event domain.Event) error {
	if a.Store == nil {
		return nil
	}
	metadata := domain.Metadata{
		"event_id":       strings.TrimSpace(event.ID),
		"channel_type":   string(event.ChannelType),
		"channel_id":     strings.TrimSpace(event.ChannelID),
		"thread_id":      strings.TrimSpace(event.ThreadID),
		"actor_id":       strings.TrimSpace(event.ActorID),
		"actor_name":     strings.TrimSpace(event.ActorName),
		"trigger_source": strings.TrimSpace(source),
	}
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			delete(metadata, key)
		}
	}
	return a.Store.UpsertOnboardingState(ctx, domain.OnboardingState{
		ProjectID: strings.TrimSpace(projectID),
		Status:    status,
		Source:    strings.TrimSpace(source),
		Metadata:  metadata,
	})
}

func (a *Activities) completeOnboardingFromMemoryTool(ctx context.Context, projectID string, event domain.Event, choice agent.ToolChoice, tags []string) error {
	if choice.Metadata == nil || !strings.EqualFold(strings.TrimSpace(choice.Metadata["onboarding"]), "true") {
		return nil
	}
	if !hasOnboardingTag(tags) {
		return nil
	}
	source := strings.TrimSpace(choice.Metadata["onboarding_source"])
	if source == "" {
		source = onboardingSourceCommand
	}
	return a.markOnboarding(ctx, projectID, domain.OnboardingStatusCompleted, source, event)
}

func hasOnboardingTag(tags []string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), "onboarding") {
			return true
		}
	}
	return false
}

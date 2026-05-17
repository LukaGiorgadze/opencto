package activities

import (
	"context"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/storage"
)

func (a *Activities) LoadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	return a.loadContext(ctx, event, event, nil)
}

func (a *Activities) loadContext(ctx context.Context, event domain.Event, conversationEvent domain.Event, additionalEvents []domain.Event) (agent.Context, error) {
	return a.loadContextWithOptions(ctx, event, conversationEvent, additionalEvents, true)
}

func (a *Activities) loadContextWithOptions(ctx context.Context, event domain.Event, conversationEvent domain.Event, additionalEvents []domain.Event, includeSkills bool) (agent.Context, error) {
	var activeWorkItems []domain.WorkItem
	if a.Store != nil {
		var err error
		activeWorkItems, err = a.Store.ListPendingWorkItems(ctx, event.ProjectID)
		if err != nil {
			return agent.Context{}, err
		}
	}
	contextEvent := inferDiscordThreadContext(conversationEvent)
	conversation, conversationSummaries, err := a.loadConversationContext(ctx, contextEvent)
	if err != nil {
		return agent.Context{}, err
	}
	var memories []domain.Memory
	if a.Store != nil && a.MemoryEnabled {
		query := strings.TrimSpace(firstNonEmpty(contextEvent.Body, event.Body))
		embeddingQuery := prepareMemoryEmbeddingQuery(query, event, additionalEvents, conversation)
		memories, err = a.searchMemoriesWithEmbeddingQuery(ctx, domain.MemorySearchRequest{
			ProjectID:      strings.TrimSpace(contextEvent.ProjectID),
			UserID:         eventUserID(contextEvent),
			ChannelType:    contextEvent.ChannelType,
			ChannelID:      strings.TrimSpace(contextEvent.ChannelID),
			ThreadID:       strings.TrimSpace(contextEvent.ThreadID),
			Query:          query,
			Scopes:         autoContextMemoryScopes(contextEvent),
			Limit:          storage.DefaultAutoContextLimit(a.MemoryLimit),
			FallbackRecent: true,
		}, embeddingQuery)
		if err != nil {
			return agent.Context{}, err
		}
		memories = excludeMemoriesFromSource(memories, event.ID)
	}

	project := a.Project
	if strings.TrimSpace(project.ID) == "" {
		project.ID = event.ProjectID
	}
	var availableSkills []skillcatalog.Summary
	if includeSkills {
		availableSkills, err = skillcatalog.Discover(ctx, a.skillsRoots()...)
		if err != nil {
			return agent.Context{}, err
		}
	}
	return agent.Context{
		Event:                       event,
		Project:                     project,
		ActiveWorkItems:             activeWorkItems,
		Memory:                      memories,
		Conversation:                conversation,
		ConversationSummaries:       conversationSummaries,
		ConversationMaxContextChars: storage.DefaultConversationMaxContextChars(a.ConversationMaxContextChars),
		Skills:                      availableSkills,
	}, nil
}

func (a *Activities) loadSubAgentContext(ctx context.Context, sourceEvent domain.Event, additionalEvents []domain.Event, subAgent agent.SubAgentContext, allowedTools []domain.ToolType) (agent.Context, error) {
	projectID := strings.TrimSpace(sourceEvent.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	sourceEvent.ProjectID = projectID
	conversationEvent := latestConversationContextEvent(sourceEvent, additionalEvents)
	loaded, err := a.loadContextWithOptions(ctx, sourceEvent, conversationEvent, additionalEvents, toolTypeInList(domain.ToolTypeSkill, allowedTools))
	if err != nil {
		return agent.Context{}, err
	}
	event := sourceEvent
	event.ID = stableActivityID("agent-event", projectID, sourceEvent.ID, subAgent.RunID, subAgent.Goal, subAgent.Prompt)
	event.ProjectID = projectID
	event.Kind = domain.EventKindSystem
	event.Body = strings.TrimSpace(prompts.MustRender("agent_user.tmpl", map[string]any{
		"Goal":   subAgent.Goal,
		"Prompt": subAgent.Prompt,
	}))
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	loaded.Event = event
	loaded.ConversationMaxContextChars = storage.DefaultConversationMaxContextChars(a.ConversationMaxContextChars)
	return loaded, nil
}

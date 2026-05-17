package activities

import (
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

func autoContextMemoryScopes(event domain.Event) []domain.MemoryScope {
	if strings.TrimSpace(event.ThreadID) != "" {
		return []domain.MemoryScope{domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
	}
	if strings.TrimSpace(event.ChannelID) != "" {
		return []domain.MemoryScope{domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
	}
	return []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
}

func conversationUserMetadata(event domain.Event) domain.Metadata {
	metadata := domain.Metadata{
		"channel_type": string(event.ChannelType),
		"channel_id":   strings.TrimSpace(event.ChannelID),
		"thread_id":    strings.TrimSpace(event.ThreadID),
		"actor_id":     strings.TrimSpace(event.ActorID),
		"actor_name":   strings.TrimSpace(event.ActorName),
	}
	if control := strings.TrimSpace(event.Metadata[domain.MetadataKeyControl]); control != "" {
		metadata[domain.MetadataKeyControl] = control
	}
	return metadata
}

func eventUserID(event domain.Event) string {
	actorID := strings.TrimSpace(event.ActorID)
	if actorID != "" {
		channelType := strings.TrimSpace(string(event.ChannelType))
		if channelType != "" {
			return channelType + ":" + actorID
		}
		return actorID
	}
	return strings.TrimSpace(event.ActorName)
}

func memoryMetadata(event domain.Event, reason string) domain.Metadata {
	metadata := domain.Metadata{
		"event_id":     strings.TrimSpace(event.ID),
		"reason":       strings.TrimSpace(reason),
		"channel_type": string(event.ChannelType),
		"channel_id":   strings.TrimSpace(event.ChannelID),
		"thread_id":    strings.TrimSpace(event.ThreadID),
		"actor_id":     strings.TrimSpace(event.ActorID),
		"actor_name":   strings.TrimSpace(event.ActorName),
	}
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			delete(metadata, key)
		}
	}
	return metadata
}

func excludeMemoriesFromSource(memories []domain.Memory, sourceID string) []domain.Memory {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || len(memories) == 0 {
		return memories
	}
	filtered := memories[:0]
	for _, memory := range memories {
		if strings.TrimSpace(memory.SourceID) == sourceID {
			continue
		}
		filtered = append(filtered, memory)
	}
	return filtered
}

func conversationRoles() []domain.ConversationRole {
	return []domain.ConversationRole{
		domain.ConversationRoleUser,
		domain.ConversationRoleAssistant,
		domain.ConversationRoleTool,
	}
}

func inferDiscordThreadContext(event domain.Event) domain.Event {
	if event.ChannelType != domain.ChannelTypeDiscord {
		return event
	}
	if strings.TrimSpace(event.ThreadID) != "" || strings.TrimSpace(event.ChannelID) == "" {
		return event
	}
	if strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]) != "" {
		return event
	}
	switch strings.TrimSpace(event.Metadata[domain.MetadataKeyControl]) {
	case domain.MetadataControlTaskReply:
		event.ThreadID = strings.TrimSpace(event.ChannelID)
	}
	return event
}

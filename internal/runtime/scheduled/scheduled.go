package scheduled

import (
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

const (
	DispatchWorkflowName       = "ScheduledDispatchWorkflow"
	EnqueueScheduledEventName  = "Activities.EnqueueScheduledEvent"
	EventMetadataScheduleID    = "schedule_id"
	EventMetadataScheduleName  = "schedule_name"
	EventMetadataScheduledAt   = "scheduled_at"
	EventMetadataCreatedBy     = "created_by_event_id"
	EventMetadataQueuePolicy   = "queue_policy"
	EventMetadataOverlapPolicy = "overlap_policy"

	QueuePolicyScheduledTask = "scheduled_task"
	OverlapPolicySkip        = "skip"
)

type DispatchWorkflowInput struct {
	ProjectID        string       `json:"project_id"`
	ScheduleID       string       `json:"schedule_id"`
	ScheduleName     string       `json:"schedule_name,omitempty"`
	Task             string       `json:"task"`
	SourceEvent      domain.Event `json:"source_event"`
	CreatedByEventID string       `json:"created_by_event_id,omitempty"`
}

type EnqueueScheduledEventRequest struct {
	Event domain.Event `json:"event"`
}

func EventFromDispatch(input DispatchWorkflowInput, eventID string, scheduledAt time.Time) domain.Event {
	event := input.SourceEvent
	event.ID = strings.TrimSpace(eventID)
	event.ProjectID = strings.TrimSpace(input.ProjectID)
	event.Kind = domain.EventKindMessage
	event.Body = strings.TrimSpace(input.Task)
	event.CreatedAt = scheduledAt.UTC()
	event.Payload = nil
	event.Metadata = mergeMetadata(event.Metadata, domain.Metadata{
		EventMetadataScheduleID:    strings.TrimSpace(input.ScheduleID),
		EventMetadataScheduleName:  strings.TrimSpace(input.ScheduleName),
		EventMetadataScheduledAt:   scheduledAt.UTC().Format(time.RFC3339Nano),
		EventMetadataCreatedBy:     strings.TrimSpace(input.CreatedByEventID),
		EventMetadataQueuePolicy:   QueuePolicyScheduledTask,
		EventMetadataOverlapPolicy: OverlapPolicySkip,
	})
	event.Provenance = domain.Provenance{
		Source:     "schedule",
		SourceID:   strings.TrimSpace(input.ScheduleID),
		Actor:      strings.TrimSpace(input.SourceEvent.ActorName),
		ObservedAt: scheduledAt.UTC(),
		Metadata: domain.Metadata{
			EventMetadataScheduleName: strings.TrimSpace(input.ScheduleName),
			EventMetadataCreatedBy:    strings.TrimSpace(input.CreatedByEventID),
		},
	}
	return event
}

func EventID(scheduleID, workflowID string, scheduledAt time.Time) string {
	parts := []string{"scheduled"}
	if value := strings.TrimSpace(scheduleID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(workflowID); value != "" {
		parts = append(parts, value)
	}
	parts = append(parts, scheduledAt.UTC().Format("20060102T150405.000000000Z"))
	return strings.Join(parts, ":")
}

func IsScheduledTaskEvent(event domain.Event) bool {
	if event.Metadata == nil {
		return false
	}
	return strings.TrimSpace(event.Metadata[EventMetadataQueuePolicy]) == QueuePolicyScheduledTask
}

func ScheduleID(event domain.Event) string {
	if event.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(event.Metadata[EventMetadataScheduleID])
}

func mergeMetadata(base domain.Metadata, updates domain.Metadata) domain.Metadata {
	out := domain.Metadata{}
	for key, value := range base {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	for key, value := range updates {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

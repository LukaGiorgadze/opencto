package scheduled

import (
	"strings"
	"testing"
	"time"
)

func TestEventIDIsCompactAndDeterministic(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, 5, 6, 14, 31, 50, 22919506, time.UTC)
	scheduleID := "opencto:default:schedule:ddce67a1-1395-49c4-9ffb-966271a019f5"
	workflowID := scheduleID + ":dispatch-2026-05-06T14:31:50Z"

	first := EventID(scheduleID, workflowID, scheduledAt)
	second := EventID(scheduleID, workflowID, scheduledAt)
	if first != second {
		t.Fatalf("expected deterministic event id, got %q and %q", first, second)
	}
	if strings.Contains(first, scheduleID) || strings.Contains(first, workflowID) {
		t.Fatalf("event id should not embed long schedule or workflow ids: %q", first)
	}
	if len(first) > 50 {
		t.Fatalf("expected compact event id, got %d chars: %q", len(first), first)
	}
	if !strings.HasPrefix(first, "scheduled:") || !strings.HasSuffix(first, ":20260506T143150.022919506Z") {
		t.Fatalf("unexpected event id format: %q", first)
	}
}

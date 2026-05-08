package workflows

import (
	"fmt"
	"testing"
)

func TestRememberProjectEventIDKeepsRecentWindow(t *testing.T) {
	t.Parallel()

	state := ProjectWorkflowState{}
	for i := 0; i < recentProjectEventIDLimit; i++ {
		if seen := rememberProjectEventID(&state, fmt.Sprintf("event-%d", i)); seen {
			t.Fatalf("new event was reported as seen")
		}
	}
	if len(state.RecentEventIDs) != recentProjectEventIDLimit {
		t.Fatalf("expected %d recent event ids, got %d", recentProjectEventIDLimit, len(state.RecentEventIDs))
	}
	if seen := rememberProjectEventID(&state, "event-0"); !seen {
		t.Fatalf("expected oldest event to still be remembered before eviction")
	}
	if seen := rememberProjectEventID(&state, "event-1000"); seen {
		t.Fatalf("new event was reported as seen")
	}
	if len(state.RecentEventIDs) != recentProjectEventIDLimit {
		t.Fatalf("expected capped recent event ids, got %d", len(state.RecentEventIDs))
	}
	if state.RecentEventIDs[0] != "event-1" {
		t.Fatalf("expected oldest event to be evicted, got first id %q", state.RecentEventIDs[0])
	}
	if seen := rememberProjectEventID(&state, "event-0"); seen {
		t.Fatalf("expected evicted event to be accepted again")
	}
}

func TestRememberProjectEventIDIgnoresBlankIDs(t *testing.T) {
	t.Parallel()

	state := ProjectWorkflowState{}
	if seen := rememberProjectEventID(&state, " "); seen {
		t.Fatalf("blank event id should not be reported as seen")
	}
	if len(state.RecentEventIDs) != 0 {
		t.Fatalf("blank event id should not be remembered")
	}
}

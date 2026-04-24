package prompts

import "testing"

func TestLoadPlanPrompt(t *testing.T) {
	t.Parallel()

	value, err := Load("plan.tmpl")
	if err != nil {
		t.Fatalf("load prompt: %v", err)
	}
	if value == "" {
		t.Fatalf("expected prompt content")
	}
}

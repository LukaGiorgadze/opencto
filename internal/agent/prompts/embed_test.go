package prompts

import "testing"

func TestLoadNextActionPrompt(t *testing.T) {
	t.Parallel()

	value, err := Load("next_action.tmpl")
	if err != nil {
		t.Fatalf("load prompt: %v", err)
	}
	if value == "" {
		t.Fatalf("expected prompt content")
	}
}

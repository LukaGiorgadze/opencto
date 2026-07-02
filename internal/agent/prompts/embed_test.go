package prompts

import "testing"

func TestLoadPrompt(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"next_action.tmpl", "agent.tmpl", "agent_email.tmpl", "conversation_compression.tmpl", "agent_observation_compression.tmpl", "agent_run_summary.tmpl"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			value, err := Load(name)
			if err != nil {
				t.Fatalf("load prompt %q: %v", name, err)
			}
			if value == "" {
				t.Fatalf("expected prompt content for %q", name)
			}
		})
	}
}

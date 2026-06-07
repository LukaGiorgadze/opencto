package workflowschedule

import (
	"strings"
	"testing"
)

func TestPromptAuthoringAgentRendersWorkflowGuidance(t *testing.T) {
	t.Parallel()

	prompt := PromptAuthoringAgent(" create ", " daily-etl ", "/tmp/workflows/daily-etl ", " build daily etl ", " create daily workflow ")

	for _, expected := range []string{
		"Operation: create",
		"Workflow ID: daily-etl",
		"Workflow directory: /tmp/workflows/daily-etl",
		"User request:\nbuild daily etl",
		"**Same-run sharing** — write to `OPENCTO_WORKFLOW_RUN_ARTIFACTS_DIR/`",
		"OPENCTO_WORKFLOW_RUN_ARTIFACTS_DIR — writable artifact directory",
		"# workflow.yml Requirements",
		"After your final response, OpenCTO automatically publishes the bundle",
		"Commit message: create daily workflow",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", expected, prompt)
		}
	}
}

func TestPromptAuthoringAgentIncludesValidManifestExampleShape(t *testing.T) {
	t.Parallel()

	prompt := PromptAuthoringAgent("create", "daily-etl", "/tmp/workflows/daily-etl", "build daily etl", "")

	for _, expected := range []string{
		"# workflow.yml Requirements",
		"Use this exact shape:",
		"  one_shot_at: \"\"",
		"  overlap_policy: skip",
		"  catchup_window: 10m",
		"  pause_on_failure: false",
		"notification_policy:\n  on_failure: true",
		"  - id: step-id",
		"    command: python3",
		"      non_retryable_error_types: []",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", expected, prompt)
		}
	}
	for _, invalid := range []string{
		"notification_policy: on_success",
		"activity:\n      command:",
		"  - name:",
		"env:\n",
		"  - GITHUB_REPO=owner/repo",
	} {
		if strings.Contains(prompt, invalid) {
			t.Fatalf("prompt still contains invalid manifest pattern %q:\n%s", invalid, prompt)
		}
	}
}

func TestPromptAuthoringAgentOmitsEmptyCommitMessage(t *testing.T) {
	t.Parallel()

	prompt := PromptAuthoringAgent("update", "daily-etl", "/tmp/workflows/daily-etl", "update workflow", " ")
	if strings.Contains(prompt, "Commit message:") {
		t.Fatalf("expected empty commit message to be omitted, got:\n%s", prompt)
	}
}

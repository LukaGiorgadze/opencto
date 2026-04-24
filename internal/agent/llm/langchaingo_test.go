package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

func TestRenderClassificationPromptUsesStructuredContext(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{
				ID:          "project-1",
				Name:        "OpenCTO",
				Description: "Self-hosted AI technical co-founder",
			},
			Event: domain.Event{
				ID:          "event-1",
				ProjectID:   "project-1",
				ActorName:   "luka",
				Body:        "deploy to staging",
				ChannelID:   "channel-1",
				ChannelType: domain.ChannelTypeDiscord,
				Metadata: map[string]string{
					"author_role":    "owner",
					"thread_context": "deployment thread",
				},
				CreatedAt: time.Date(2026, 4, 23, 9, 30, 0, 0, time.UTC),
			},
			OpenContradictions: []domain.PendingContradiction{{
				ID:        "contr-1",
				Topic:     "database choice",
				Status:    domain.ContradictionStatusOpen,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			ActiveWorkItems: []domain.WorkItem{{
				ID:        "wi-1",
				Title:     "Ship staging deployment",
				Status:    domain.WorkItemStatusReady,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			ConversationMemory: []domain.MemoryFact{{
				ID:        "memory-1",
				Value:     "The agent asked which staging target should receive the deploy.",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			RecentDecisions: []domain.ADR{{
				ID:        "adr-1",
				Title:     "Use Temporal",
				Summary:   "Temporal coordinates long-running work.",
				CreatedAt: time.Now().UTC(),
			}},
		},
	}

	prompt, err := renderClassificationPrompt(input)
	if err != nil {
		t.Fatalf("render classification prompt: %v", err)
	}

	for _, want := range []string{
		"Project: OpenCTO",
		"Project Description: Self-hosted AI technical co-founder",
		"Active work: Ship staging deployment [ready]",
		"Open contradictions: database choice",
		"Recent conversation: The agent asked which staging target should receive the deploy.",
		"Recent decisions: Use Temporal: Temporal coordinates long-running work.",
		"Author: luka (role: owner)",
		"Channel: discord:channel-1",
		"Thread context: deployment thread",
		"Message: deploy to staging",
		"Timestamp: 2026-04-23T09:30:00Z",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatalf("prompt still contains template markers:\n%s", prompt)
	}
	if strings.Contains(prompt, "Attachments:") {
		t.Fatalf("prompt still contains removed attachments field:\n%s", prompt)
	}
}

func TestNormalizeClassificationClarifiesOnOpenContradictions(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "deploy to prod",
			},
			OpenContradictions: []domain.PendingContradiction{{
				ID:        "contr-1",
				Topic:     "deployment target",
				Status:    domain.ContradictionStatusOpen,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:     agent.ClassificationIntentActionRequest,
		Tier:       domain.RiskTierOwnerApproval,
		Confidence: 0.9,
		RoutedTo:   agent.ClassificationRoutePlan,
		Summary:    "Production deployment requested.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if !classification.RequiresClarification() {
		t.Fatalf("expected clarification requirement")
	}
	if classification.RoutedTo != agent.ClassificationRouteClarify {
		t.Fatalf("expected clarify route, got %q", classification.RoutedTo)
	}
}

func TestNormalizeClassificationKeepsQuestionRouteAndClampsConfidence(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "what changed?",
			},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:     agent.ClassificationIntentQuestion,
		RoutedTo:   agent.ClassificationRouteAnswer,
		Confidence: 1.4,
		Summary:    "User asked a question.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if classification.RoutedTo != agent.ClassificationRouteAnswer {
		t.Fatalf("expected answer route, got %q", classification.RoutedTo)
	}
	if classification.Tier != domain.RiskTierObserve {
		t.Fatalf("expected observe tier, got %d", classification.Tier)
	}
	if classification.Confidence != 1 {
		t.Fatalf("expected confidence clamp to 1, got %f", classification.Confidence)
	}
}

func TestNormalizeClassificationRejectsMissingSummary(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "deploy to staging",
			},
		},
	}

	_, err := normalizeClassification(input, agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		Tier:     domain.RiskTierSafeLocalChange,
		RoutedTo: agent.ClassificationRoutePlan,
	})
	if err == nil {
		t.Fatalf("expected validation error for missing summary")
	}
}

func TestNormalizeClassificationPromotesClarificationFollowUpToPlan(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "yes, you should initialize git inside `/Users/luka/projects/opencto/hello-world` and remove the root repo",
			},
			ConversationMemory: []domain.MemoryFact{{
				ID:        "memory-1",
				Value:     "I need one quick clarification to continue: should I initialize a git repo inside hello-world specifically, and if so, what is the exact path?",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			RecentDecisions: []domain.ADR{{
				ID:        "adr-1",
				Title:     "Execution Summary",
				Summary:   "The repo was initialized in the project root instead of hello-world. I need one quick clarification to continue.",
				CreatedAt: time.Now().UTC(),
			}},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:            agent.ClassificationIntentContextUpdate,
		RoutedTo:          agent.ClassificationRouteIngest,
		ContradictionRisk: true,
		Summary:           "Git setup instruction update.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if classification.Intent != agent.ClassificationIntentActionRequest {
		t.Fatalf("expected action request intent, got %q", classification.Intent)
	}
	if classification.RoutedTo != agent.ClassificationRoutePlan {
		t.Fatalf("expected plan route, got %q", classification.RoutedTo)
	}
	if classification.Tier != domain.RiskTierSafeLocalChange {
		t.Fatalf("expected safe local change tier, got %d", classification.Tier)
	}
}

func TestNormalizeClassificationPromotesPathOnlyClarificationAnswerToPlan(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "`/Users/luka/projects/opencto/hello-world`",
			},
			ConversationMemory: []domain.MemoryFact{{
				ID:    "memory-1",
				Value: "assistant: I’m moving the git repo into hello-world. What is the exact path to that folder from the project root?",
				Metadata: map[string]string{
					"speaker": "assistant",
				},
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:             agent.ClassificationIntentNeutral,
		RoutedTo:           agent.ClassificationRouteClarify,
		NeedsClarification: true,
		Summary:            "User shared filesystem path; no explicit request.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if classification.Intent != agent.ClassificationIntentActionRequest {
		t.Fatalf("expected action request intent, got %q", classification.Intent)
	}
	if classification.RoutedTo != agent.ClassificationRoutePlan {
		t.Fatalf("expected plan route, got %q", classification.RoutedTo)
	}
	if classification.NeedsClarification {
		t.Fatalf("expected clarification requirement to be cleared")
	}
}

func TestNormalizeClassificationPromotesApprovalFollowUpToPlan(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "yes",
			},
			ConversationMemory: []domain.MemoryFact{{
				ID:    "memory-1",
				Value: "assistant: I have the hello-world path. Should I initialize git there and remove the root .git in /Users/luka/projects/opencto?",
				Metadata: map[string]string{
					"speaker": "assistant",
				},
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:             agent.ClassificationIntentApproval,
		RoutedTo:           agent.ClassificationRouteClarify,
		NeedsClarification: true,
		Summary:            "User confirms/approves git clarification response.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if classification.Intent != agent.ClassificationIntentActionRequest {
		t.Fatalf("expected action request intent, got %q", classification.Intent)
	}
	if classification.RoutedTo != agent.ClassificationRoutePlan {
		t.Fatalf("expected plan route, got %q", classification.RoutedTo)
	}
	if classification.NeedsClarification {
		t.Fatalf("expected clarification requirement to be cleared")
	}
}

func TestNormalizeClassificationKeepsStandaloneContextUpdateAsIngest(t *testing.T) {
	t.Parallel()

	input := agent.DecisionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "here's the Supabase project URL: https://example.supabase.co",
			},
		},
	}

	classification, err := normalizeClassification(input, agent.Classification{
		Intent:   agent.ClassificationIntentContextUpdate,
		RoutedTo: agent.ClassificationRouteIngest,
		Summary:  "Supabase URL provided.",
	})
	if err != nil {
		t.Fatalf("normalizeClassification: %v", err)
	}

	if classification.Intent != agent.ClassificationIntentContextUpdate {
		t.Fatalf("expected context update intent, got %q", classification.Intent)
	}
	if classification.RoutedTo != agent.ClassificationRouteIngest {
		t.Fatalf("expected ingest route, got %q", classification.RoutedTo)
	}
	if classification.Tier != domain.RiskTierObserve {
		t.Fatalf("expected observe tier, got %d", classification.Tier)
	}
}

func TestRenderClarificationPromptUsesAvailableContextOnly(t *testing.T) {
	t.Parallel()

	input := agent.ClarificationInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{
				ID:          "project-1",
				Name:        "OpenCTO",
				Description: "Self-hosted AI technical co-founder",
			},
			Event: domain.Event{
				ID:          "event-2",
				ProjectID:   "project-1",
				ActorName:   "luka",
				Body:        "deploy it",
				ChannelID:   "channel-1",
				ChannelType: domain.ChannelTypeDiscord,
				Metadata: map[string]string{
					"author_role": "owner",
				},
			},
			ProjectFacts: []domain.MemoryFact{{
				ID:        "fact-1",
				Key:       "deployment_target",
				Value:     "vercel",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			OpenContradictions: []domain.PendingContradiction{{
				ID:        "contr-1",
				Topic:     "deployment target",
				Status:    domain.ContradictionStatusOpen,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			ConversationMemory: []domain.MemoryFact{{
				ID:    "memory-1",
				Value: "Which environment should I deploy to: staging or production?",
				Metadata: map[string]string{
					"speaker": "assistant",
				},
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
		},
		Classification: agent.Classification{
			Intent:             agent.ClassificationIntentActionRequest,
			Tier:               domain.RiskTierOwnerApproval,
			Confidence:         0.91,
			NeedsClarification: true,
			ContradictionRisk:  true,
			RoutedTo:           agent.ClassificationRouteClarify,
			Summary:            "Deployment target is unclear.",
		},
	}

	prompt, err := renderClarificationPrompt(input)
	if err != nil {
		t.Fatalf("render clarification prompt: %v", err)
	}

	for _, want := range []string{
		"Project: OpenCTO (project-1)",
		"Known facts: deployment_target: vercel",
		"Open contradictions: deployment target",
		"Recent conversation: assistant: Which environment should I deploy to: staging or production?",
		"Author: luka (role: owner)",
		"Message: deploy it",
		"Classifier intent: ACTION_REQUEST",
		"Classifier route: clarify",
		"Classifier contradiction risk: true",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"Prior clarification rounds:",
		"Previously asked:",
		"Answers received:",
		"{{",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt still contains unsupported content %q\n%s", unwanted, prompt)
		}
	}
}

func TestNormalizeClarificationOutputBuildsReason(t *testing.T) {
	t.Parallel()

	input := agent.ClarificationInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Event: domain.Event{
				ProjectID: "project-1",
				Body:      "deploy it",
			},
		},
		Classification: agent.Classification{
			Summary: "Deployment target is unclear.",
		},
	}

	request, err := normalizeClarificationOutput(input, clarificationLLMOutput{
		KnownSummary: "A deployment was requested.",
		BlockingGaps: []string{"target environment is missing", "", "deployment scope is missing", "extra"},
		Assumptions:  []string{"use the existing branch", "", "stay in the current repo", "extra"},
		Questions:    []string{"Which environment should I target?", "", "second", "third", "extra"},
		Message:      "I have a deployment request. Which environment should I target?",
	})
	if err != nil {
		t.Fatalf("normalizeClarificationOutput: %v", err)
	}

	if request.KnownSummary != "A deployment was requested." {
		t.Fatalf("unexpected known summary: %q", request.KnownSummary)
	}
	if len(request.BlockingGaps) != 3 {
		t.Fatalf("expected trimmed blocking gaps, got %v", request.BlockingGaps)
	}
	if len(request.Assumptions) != 3 {
		t.Fatalf("expected trimmed assumptions, got %v", request.Assumptions)
	}
	if len(request.Questions) != 3 {
		t.Fatalf("expected trimmed questions, got %v", request.Questions)
	}
	if !strings.Contains(request.Reason, "Blocked by: target environment is missing; deployment scope is missing; extra") {
		t.Fatalf("unexpected reason: %q", request.Reason)
	}
	if request.Message == "" {
		t.Fatalf("expected message to be preserved")
	}
}

func TestRenderPlanningPromptUsesSupportedContext(t *testing.T) {
	t.Parallel()

	input := agent.PlanningInput{
		ProjectID:         "project-1",
		AutonomyThreshold: 1,
		AvailableSkills:   []string{"nextjs", "supabase"},
		Context: agent.Context{
			Project: domain.Project{
				ID:          "project-1",
				Name:        "OpenCTO",
				Description: "Self-hosted AI technical co-founder",
			},
			Event: domain.Event{
				ID:        "event-3",
				ProjectID: "project-1",
				ActorName: "luka",
				Body:      "add email verification to signup",
				Metadata: map[string]string{
					"author_role":           "owner",
					"clarification_summary": "Target the existing signup flow.",
					"resolved_answers":      "Use Supabase Auth and ship to staging first.",
				},
			},
			ProjectFacts: []domain.MemoryFact{{
				ID:        "fact-1",
				Key:       "auth_provider",
				Value:     "supabase",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			ConversationMemory: []domain.MemoryFact{{
				ID:        "memory-1",
				Value:     "The agent asked whether to target the existing signup flow and which environment to ship first.",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			Integrations: []domain.Integration{{
				ID:        "integration-1",
				Kind:      "vercel",
				Status:    domain.IntegrationStatusReady,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			RecentDecisions: []domain.ADR{{
				ID:        "adr-1",
				Title:     "Deployment Summary",
				Summary:   "Deployed the staging environment to Vercel.",
				CreatedAt: time.Now().UTC(),
			}},
		},
		Classification: agent.Classification{
			Intent:   agent.ClassificationIntentActionRequest,
			RoutedTo: agent.ClassificationRoutePlan,
			Tier:     domain.RiskTierConsequential,
		},
	}

	prompt, err := renderPlanningPrompt(input)
	if err != nil {
		t.Fatalf("render planning prompt: %v", err)
	}

	for _, want := range []string{
		"Project: OpenCTO (project-1)",
		"Known facts: auth_provider: supabase",
		"Recent conversation: The agent asked whether to target the existing signup flow and which environment to ship first.",
		"Integrations: vercel [ready]",
		"Autonomy Threshold: 1",
		"Clarification summary: Target the existing signup flow.",
		"Resolved answers: Use Supabase Auth and ship to staging first.",
		"Available skills: nextjs, supabase",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatalf("prompt still contains template markers:\n%s", prompt)
	}
}

func TestNormalizePlanningOutputMapsPlanMetadataAndWorkItems(t *testing.T) {
	t.Parallel()

	input := agent.PlanningInput{
		ProjectID:         "project-1",
		AutonomyThreshold: 1,
		Context: agent.Context{
			Event: domain.Event{
				ID:        "event-4",
				ProjectID: "project-1",
				Body:      "add email verification to signup",
			},
		},
		Classification: agent.Classification{
			Intent:   agent.ClassificationIntentActionRequest,
			RoutedTo: agent.ClassificationRoutePlan,
			Tier:     domain.RiskTierConsequential,
			Summary:  "Email verification requested.",
		},
	}

	output, err := normalizePlanningOutput(input, planningLLMOutput{
		PlanSummary:      "Add email verification to the existing signup flow and validate it on staging.",
		Assumptions:      []string{"Use Supabase Auth", "", "Stay on the existing branch"},
		Risks:            []string{"Email rate limits may slow validation"},
		RequiresApproval: true,
		ExecutionOrder: [][]string{
			{"Audit Supabase CLI dependency"},
			{"Enable email verification in Supabase"},
		},
		TestStrategy: "Verify the signup flow on staging with a real test email.",
		WorkItems: []planningLLMWorkItem{
			{
				Title:              "Audit Supabase CLI dependency",
				Description:        "Check the CLI package status before using it.",
				AcceptanceCriteria: []string{"Registry stats reviewed", "Last publish date checked"},
				Rollback:           "",
				Skills:             []string{"supabase"},
				ToolHint:           "shell",
				Tier:               0,
				RequiresApproval:   false,
				DependsOn:          nil,
				Complexity:         "S",
			},
			{
				Title:              "Enable email verification in Supabase",
				Description:        "Turn on email confirmation in the existing auth config.",
				AcceptanceCriteria: []string{"Email confirmation is enabled"},
				Rollback:           "Revert the auth config change.",
				Skills:             []string{"supabase"},
				ToolHint:           "shell",
				Tier:               2,
				RequiresApproval:   true,
				DependsOn:          []string{"Audit Supabase CLI dependency"},
				Complexity:         "M",
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize planning output: %v", err)
	}

	if output.Plan.Summary != "Add email verification to the existing signup flow and validate it on staging." {
		t.Fatalf("unexpected plan summary: %q", output.Plan.Summary)
	}
	if _, ok := output.Plan.Metadata["discord_message"]; ok {
		t.Fatalf("did not expect channel-specific review copy in metadata: %#v", output.Plan.Metadata)
	}
	if output.Plan.Metadata["requires_approval"] != "true" {
		t.Fatalf("expected requires approval metadata, got %#v", output.Plan.Metadata)
	}
	if output.Plan.Metadata["test_strategy"] != "Verify the signup flow on staging with a real test email." {
		t.Fatalf("unexpected test strategy metadata: %#v", output.Plan.Metadata)
	}
	if got := decodeJSONMetadataList(output.Plan.Metadata["assumptions_json"]); len(got) != 2 {
		t.Fatalf("expected trimmed assumptions metadata, got %v", got)
	}
	if got := decodeJSONMetadataMatrix(output.Plan.Metadata["execution_order_json"]); len(got) != 2 {
		t.Fatalf("expected execution order metadata, got %v", got)
	}
	if len(output.WorkItems) != 2 {
		t.Fatalf("expected 2 work items, got %d", len(output.WorkItems))
	}
	if output.WorkItems[1].RiskTier != domain.RiskTierConsequential {
		t.Fatalf("unexpected risk tier: %d", output.WorkItems[1].RiskTier)
	}
	if output.WorkItems[1].Metadata["requires_approval"] != "true" {
		t.Fatalf("expected work item approval metadata, got %#v", output.WorkItems[1].Metadata)
	}
	if got := decodeJSONMetadataList(output.WorkItems[1].Metadata["depends_on_json"]); len(got) != 1 || got[0] != "Audit Supabase CLI dependency" {
		t.Fatalf("unexpected dependency metadata: %v", got)
	}
	if got := decodeJSONMetadataList(output.WorkItems[1].Metadata["acceptance_criteria_json"]); len(got) != 1 {
		t.Fatalf("unexpected acceptance criteria metadata: %v", got)
	}
	if len(output.Plan.Steps) != 2 {
		t.Fatalf("expected 2 plan steps, got %d", len(output.Plan.Steps))
	}
	if output.Plan.Steps[1].ToolHint != domain.ToolTypeShell {
		t.Fatalf("expected shell tool hint, got %q", output.Plan.Steps[1].ToolHint)
	}
}

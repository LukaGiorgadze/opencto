package planning

import (
	_ "embed"
	"encoding/json"
)

const (
	AskUserQuestionToolName        = "AskUserQuestion"
	AskUserQuestionToolDescription = `Ask the user one consequential planning question before acting.

Use this only when the answer materially changes what OpenCTO should build, skip, risk, or trade off. Ask one question at a time, provide 2-4 concrete options, and make the recommended option first with "(Recommended)" in the label.`

	ProposePlanToolName        = "ProposePlan"
	ProposePlanToolDescription = `Present a CTO-style implementation plan for explicit approval before non-trivial mutation.

Use this after enough read-only exploration and requirement gathering. The plan must explain what to build, what to skip, key risks, tradeoffs, architecture, execution steps, and verification.`
)

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type AskUserQuestionRequest struct {
	Header   string           `json:"header"`
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options"`
}

type ProposePlanRequest struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Build        []string `json:"build"`
	Skip         []string `json:"skip"`
	Risks        []string `json:"risks"`
	Tradeoffs    []string `json:"tradeoffs"`
	Architecture []string `json:"architecture"`
	Steps        []string `json:"steps"`
	Verification []string `json:"verification"`
}

//go:embed ask_user_question_schema.json
var askUserQuestionToolSchema json.RawMessage

//go:embed propose_plan_schema.json
var proposePlanToolSchema json.RawMessage

func AskUserQuestionToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), askUserQuestionToolSchema...)
}

func ProposePlanToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), proposePlanToolSchema...)
}

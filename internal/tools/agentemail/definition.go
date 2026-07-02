package agentemail

import (
	_ "embed"
	"encoding/json"
)

const (
	AgentEmailToolName        = "AgentEmail"
	AgentEmailToolDescription = `Creates and operates OpenCTO's dedicated AgentEmail inbox for third-party service account workflows.

Use this tool when the user chooses OpenCTO's agent-owned email inbox for services such as Cloudflare, Vercel, GitHub, registrars, or hosting providers. It can create or reuse the AgentMail-backed inbox, list/search/read/wait for messages, and send email from the inbox. Do not use this tool for the user's own email account; if the user chooses to authenticate with their own provider account, skip AgentEmail.

This tool does not store durable memory. When setup returns a memory suggestion, use MemoryProposeAdd or MemoryProposeUpdate to save the non-secret email facts. Never store API keys, passwords, OTPs, cookies, or recovery secrets.`
)

//go:embed schema.json
var agentEmailToolSchema json.RawMessage

func AgentEmailToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), agentEmailToolSchema...)
}

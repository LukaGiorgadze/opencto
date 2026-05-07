package browser

import (
	_ "embed"
	"encoding/json"
)

const (
	BrowserToolName        = "Browser"
	BrowserToolDescription = `Control a real browser when a full browsing context is needed — dynamic pages, auth flows, UI interaction, and visual verification.
Use for navigation, clicks, form filling, screenshots, PDFs, accessibility and DOM snapshots, console and network inspection, cookies, localStorage, tabs, frames, dialogs, and React devtools.
Prefer snapshot over screenshots for page structure — it is faster. Chain actions within a session instead of opening new ones. Reuse an active session if one exists.
Choose a meaningful non-empty session name for browser flows, for example appstore-login. OpenCTO prefixes it with opencto-<project_id>-. Empty session uses opencto-<project_id>.
Don't use when simpler tools suffice — read local files with Read, search with Grep/Glob, fetch a single URL with curl/wget.`
)

//go:embed schema.json
var browserToolSchema json.RawMessage

func BrowserToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), browserToolSchema...)
}

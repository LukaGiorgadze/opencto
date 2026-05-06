package browser

import (
	_ "embed"
	"encoding/json"
)

const (
	BrowserToolName        = "Browser"
	BrowserToolDescription = `Control a browser to accomplish the current goal.

**IMPORTANT:** Avoid using this tool for tasks that can be done without a browser. Prefer dedicated tools when available:

- Read local files: Use Read (NOT browser file:// URLs)
- Search content: Use Grep or Glob (NOT browser-based scraping)
- Run scripts: Use Exec (NOT browser console evaluation)
- Fetch a single URL's raw content: Use Exec + curl/wget (NOT a full browser session)

Use this tool when you genuinely need a browser: JavaScript-rendered pages, auth flows, UI interaction, visual verification, or anything that requires a real browsing context.

This tool runs Vercel's agent-browser directly (not through a exec). Provide one agent-browser subcommand in command and its arguments in args. OpenCTO places known global flags before the command.

## Examples of what you can do:
- Navigate to URLs and interact with pages (open/goto/navigate, click, fill, type, press, hover, select, check, scroll, drag, upload)
- Take screenshots, save PDFs, and inspect visual diffs
- Capture accessibility snapshots and assert on DOM state
- Inspect console output and network requests
- Manage tabs, frames, dialogs, cookies, localStorage, sessionStorage, and browser state
- Record traces and profiles for debugging
- Use React inspection and Web Vitals commands after launching with --enable react-devtools

## Instructions
- Use an empty session for the default task session. Never pass --session or --session=... in args.
- All schema fields are required; use empty strings, empty arrays, 0, or false when a field is not applicable.
- The session field maps to agent-browser's isolated --session value. For persistent saved auth state, pass --session-name in args or configure sessionName in agent-browser.json.
- Configuration fields such as headed, json, debug, profile, state, proxy, userAgent, provider, device, engine, screenshotDir, allowedDomains, maxOutput, and noAutoDialog are agent-browser config settings, not top-level OpenCTO tool fields. Pass the equivalent CLI flags in args or use an agent-browser config file.
- Common global args include --json, --headed, --debug, --profile, --state, --headers, --session-name, --executable-path, --extension, --init-script, --enable, --args, --user-agent, --proxy, --proxy-bypass, --ignore-https-errors, --allow-file-access, -p/--provider, --device, --annotate, --screenshot-dir, --screenshot-quality, --screenshot-format, --cdp, --auto-connect, --color-scheme, --download-path, --content-boundaries, --max-output, --allowed-domains, --action-policy, --confirm-actions, --confirm-interactive, --engine, --no-auto-dialog, --model, -v/--verbose, -q/--quiet, and --config.
- Prefer snapshot -i --json for machine-readable page structure, then interact with refs such as @e2. Re-snapshot after navigation or UI changes.
- Prefer accessibility snapshots over screenshots when you only need to read page structure; they are faster and cheaper.
- Chain actions within a session rather than opening new sessions for each step.
- Use batch for multi-step flows that do not require inspecting intermediate output, for example command=batch args=["open https://example.com", "wait --load networkidle", "snapshot -i --json"].
- If a page requires authentication, restore a saved storage state rather than logging in from scratch on every run.
- Prefer refs from snapshot output (for example @e2) and semantic locators over CSS/XPath when selectors are unstable.
- Use wait with selectors, text, URLs, load states, or JavaScript predicates after actions that trigger page loads before asserting state.
- Use --allow-file-access only when a file:// workflow genuinely needs local file access.
- Use --content-boundaries, --max-output, --allowed-domains, --action-policy, and --confirm-actions when browsing untrusted or high-risk pages.
- The tool runs from the project workspace and returns stdout, stderr, exit code, session, command metadata, and detected artifact paths.

**IMPORTANT:** Do not open a new browser session if one is already active for this task — reuse it.`
)

//go:embed schema.json
var browserToolSchema json.RawMessage

func BrowserToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), browserToolSchema...)
}

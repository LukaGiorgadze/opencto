---
name: agent-browser
description: Browser automation guidance for OpenCTO. Use when the user needs to interact with websites, including navigating pages, filling forms, clicking buttons, taking screenshots, extracting data, testing web apps, logging in to a site, or automating any browser task. In OpenCTO, use the Browser tool for actual browser actions. Use Exec only for agent-browser documentation or diagnostics commands that the Browser tool does not cover.
allowed-tools: Browser Glob Grep Read Exec
compatibility: Requires agent-browser CLI
hidden: true
---

# agent-browser

Fast browser automation CLI for AI agents. Chrome/Chromium via CDP with
accessibility-tree snapshots and compact `@eN` element refs.

Install: `pnpm add -g agent-browser && agent-browser install`

## Start here

This file is OpenCTO guidance for the `agent-browser` CLI. OpenCTO has a
dedicated Browser tool that wraps `agent-browser`; use Browser for actual
browser actions such as open, snapshot, click, fill, type, wait, screenshot,
cookies, storage, tab, close, and similar commands.

Do not use Exec to run normal browser actions like:

```bash
agent-browser open https://example.com
agent-browser snapshot -i
agent-browser click @e1
```

Translate them to Browser tool calls instead:

```json
{"command":"open","args":["--headed","https://example.com"],"session":"example-login"}
{"command":"snapshot","args":["-i"],"session":"example-login"}
{"command":"click","args":["@e1"],"session":"example-login"}
```

Use `--headed` in Browser args when the user asks to see the browser, enter
credentials, approve a login, or otherwise interact manually.

## CLI workflow reference

For complex flows, refs, troubleshooting, or command details, load the
version-matched workflow content from the CLI with Exec:

```bash
agent-browser skills get core             # start here — workflows, common patterns, troubleshooting
agent-browser skills get core --full      # include full command reference and templates
```

Use that output as guidance only. Continue executing browser actions with the
Browser tool unless you are running documentation or diagnostic commands such
as `agent-browser skills get`, `agent-browser doctor`, or
`agent-browser profiles`.

## Specialized skills

Load a specialized skill when the task falls outside browser web pages:

```bash
agent-browser skills get electron          # Electron desktop apps (VS Code, Slack, Discord, Figma, ...)
agent-browser skills get slack             # Slack workspace automation
agent-browser skills get dogfood           # Exploratory testing / QA / bug hunts
agent-browser skills get vercel-sandbox    # agent-browser inside Vercel Sandbox microVMs
agent-browser skills get agentcore         # AWS Bedrock AgentCore cloud browsers
```

Run `agent-browser skills list` to see everything available on the
installed version.

## Why agent-browser

- Fast native Rust CLI, not a Node.js wrapper
- Works with any AI agent (Cursor, Claude Code, Codex, Continue, Windsurf, etc.)
- Chrome/Chromium via CDP with no Playwright or Puppeteer dependency
- Accessibility-tree snapshots with element refs for reliable interaction
- Sessions, authentication vault, state persistence, video recording
- Specialized skills for Electron apps, Slack, exploratory testing, cloud providers

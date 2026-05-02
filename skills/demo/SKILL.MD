---
name: demo
description: Show visual proof of a change, feature, or bug fix by detecting the environment, launching the app if needed, capturing a screenshot, and storing it.
allowed-tools: Glob Grep Read Shell
compatibility: Requires Codex CLI
---

# Demo Proof Skill

Use this skill to produce concrete proof of work after a user asks to see, verify, preview, demo, or capture the result of a change.

The goal is not just to take a screenshot. The goal is to capture focused evidence that shows the requested change, feature, bug fix, app state, or output.

## Core Rule

Do not claim a visual or runtime change is verified unless the captured artifact clearly proves it.

Every proof attempt must produce one of:

- focused screenshot
- browser screenshot
- simulator/device screenshot
- app-window screenshot
- terminal output/log
- test/build output
- accessibility snapshot
- video, if screenshots are insufficient

If proof is inconclusive, say so clearly.

## Workflow

Follow this process:

1. Understand what the user wants proven.
2. Inspect the project or recent changes when needed.
3. Select the most specific viable strategy.
4. Check required tools for that strategy.
5. Open, start, or reuse the relevant runtime.
6. Navigate to the changed area.
7. Wait for the target state to render.
8. Capture focused evidence.
9. Save evidence under `./screenshots/`.
10. Present the artifact and verification result to the user.

## Strategy Selection

Prefer the most domain-native strategy.

Use this priority order:

1. `web` — browser-based app, such as React, Next.js, Vite, Remix, static site, dashboard, web UI etc.
2. `simulator` — iOS Simulator, Android emulator, Expo, React Native, native mobile app
3. `desktop` — macOS app, Electron app, native desktop UI, visible application window
4. `terminal` — CLI app, script output, server logs, tests, build result
5. `fallback` — when no visual runtime can be detected or required tools are missing

Do not use a full-desktop screenshot when a more focused capture method exists.

## Strategy References

Each strategy has a dedicated reference file. Before executing a strategy, read the corresponding file — it contains required tools, setup steps, commands, and capture instructions specific to that environment.

| Strategy  | Reference               |
| --------- | ----------------------- |
| web       | references/web.md       |
| simulator | references/simulator.md |
| desktop   | references/desktop.md   |
| terminal  | references/terminal.md  |
| fallback  | references/fallback.md  |

Do not proceed with a strategy if its reference file does not exist. Notify the user and fall back to the next viable strategy.

---

## Notes

- Always wait for the app to fully render before screenshotting (add delay or poll for element if using Playwright)
- Never take a full-desktop screenshot — always target the specific app window or viewport
- If multiple windows of the same app are open, prefer the frontmost one

---
name: demo
description: Show visual proof of a change, feature, or bug fix by detecting the environment, launching the app if needed, capturing a screenshot, and storing it.
allowed-tools: Glob Grep Read Shell
compatibility: Requires Codex CLI
---

# Core Rule

The goal is to capture focused evidence that shows the requested change, feature, bug fix, app state, or output.
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

Prefer the most domain-native strategy. **Before executing, read the corresponding reference file — it contains required tools, setup steps, commands, and capture instructions for that environment.**

| Strategy    | When to use                                                             | Reference               |
| ----------- | ----------------------------------------------------------------------- | ----------------------- |
| `web`       | Browser-based app — React, Next.js, Vite, Remix, static site, dashboard | references/web.md       |
| `simulator` | iOS Simulator, Android emulator, Expo, React Native, native mobile app  | references/simulator.md |
| `desktop`   | macOS app, Electron app, native desktop UI, visible application window  | references/desktop.md   |
| `terminal`  | CLI app, script output, server logs, tests, build result                | references/terminal.md  |
| `fallback`  | No visual runtime detected or required tools are missing                | references/fallback.md  |

Do not use a full-desktop screenshot when a more focused capture method exists.
Do not proceed with a strategy if its reference file does not exist — notify the user and fall back to the next viable strategy.

---

## Notes

- Always wait for the app to fully render before screenshotting (add delay or poll for element if using Playwright)
- Never take a full-desktop screenshot — always target the specific app window or viewport
- If multiple windows of the same app are open, prefer the frontmost one

---
name: show-me
description: Show visual proof of a change, feature, or bug fix by detecting the environment, launching the app if needed, capturing a screenshot, and storing it.
allowed-tools: Exec Glob Grep Read Browser
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
9. Save evidences under `$OPENCTO_WORKSPACE/screenshots/` unless the user asks for another location.
10. Present the artifact and verification result to the user.

This skill's references and scripts live under `$OPENCTO_ROOT/skills/show-me/`.
Read reference files and run helper scripts from there. Save proof artifacts
under `$OPENCTO_WORKSPACE/screenshots/` unless the user asks for another
location. `$OPENCTO_WORKSPACE` is required and comes from `config.json`.

## Strategy Selection

Prefer the most domain-native strategy. **Before executing, read the corresponding reference file — it contains required tools, setup steps, commands, and capture instructions for that environment.**

| Strategy    | When to use                                                             | Reference               |
| ----------- | ----------------------------------------------------------------------- | ----------------------- |
| `web`       | Browser-based app — React, Next.js, Vite, Remix, static site, dashboard | `$OPENCTO_ROOT/skills/show-me/references/web.md`       |
| `simulator` | iOS Simulator, Android emulator, Expo, React Native, native mobile app  | `$OPENCTO_ROOT/skills/show-me/references/simulator.md` |
| `desktop`   | macOS app, Electron app, native desktop UI, visible application window  | `$OPENCTO_ROOT/skills/show-me/references/desktop.md`   |
| `terminal`  | CLI app, script output, server logs, tests, build result                | `$OPENCTO_ROOT/skills/show-me/references/terminal.md`  |
| `fallback`  | No visual runtime detected or required tools are missing                | `$OPENCTO_ROOT/skills/show-me/references/fallback.md`  |

Do not use a full-desktop screenshot when a more focused capture method exists.
Do not proceed with a strategy if its reference file does not exist — notify the user and fall back to the next viable strategy.

## Window Control Helper

Use `$OPENCTO_ROOT/skills/show-me/scripts/winctl.py` for desktop app window discovery, app launching, focusing, and focused app-window screenshots.

Command reference:

- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" list` — list running apps and visible/minimized/maximized/hidden windows detected by PyWinCtl.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" open <AppName>` — launch the app if closed, or reopen it if it is running with no visible windows. On macOS this handles the red-window-button state by sending a `reopen` event. Aliases: `launch`, `start`.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" focus <AppName>` — bring an already-running app/window to the front. Use this when a window already exists.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" restore <AppName>` — restore and focus an existing minimized/non-normal window. This does not launch an app or create a window.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" minimize <AppName>` — minimize the first matching window. Alias: `min`.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" maximize <AppName>` — restore if needed, then maximize the first matching window. Alias: `max`.
- `python "$OPENCTO_ROOT/skills/show-me/scripts/winctl.py" screenshot [--front] <AppName> [output.png]` — capture the first matching app window. Use `--front` to restore/activate before capture. If no output path is given, the helper saves under `$OPENCTO_WORKSPACE/screenshots/`.

Prefer `open` when the app may be closed or may be running without visible windows. Prefer `focus` when the app already has a visible window and you only need to focus it. Prefer `restore` when the window exists but is minimized.

---

## Notes

- Always wait for the app to fully render before screenshotting (add delay or poll for element if using Web/Browser)
- Never take a full-desktop screenshot — always target the specific app window or viewport
- If multiple windows of the same app are open, prefer the frontmost one

# Terminal Evidence

Use this strategy when the terminal itself is the user-facing surface or the most direct proof of behavior.

Read this file from `$OPENCTO_ROOT/skills/show-me/references/terminal.md`.
Run terminal commands in `PROJECT_ROOT` unless the command is explicitly reading
OpenCTO skill references or helper scripts from `OPENCTO_ROOT`. Store terminal
proof artifacts under `$OPENCTO_WORKSPACE/screenshots/` unless the user asks for
another location. `$OPENCTO_WORKSPACE` is required and comes from `config.json`.

Good fits:

- CLI tools, scripts, installers, migrations, seeders, generators, and one-off commands
- test, build, lint, typecheck, benchmark, or deployment output
- terminal UIs where layout, colors, prompts, spinners, progress bars, or interactive state matter
- server logs that prove a request, event, job, webhook, or error path happened
- shell workflows where the exact command and resulting output are the evidence

Prefer plain captured command output over a screenshot when the important proof is only text and no terminal layout or visual state matters. Use a screenshot when the visual presentation matters, when the user explicitly asks to see it, or when the output is easier to trust as a focused terminal window than as copied text.

## Capture Guidance

Before capturing, make the terminal evidence focused and readable:

- Show the command that produced the result when it is relevant.
- Let long-running commands finish or reach the stable state being proven.
- Clear unrelated scrollback when it would confuse the evidence.
- Keep enough surrounding lines to show cause and effect, but avoid dumping unrelated logs.
- If output is very long, capture the final meaningful section and summarize what was omitted.
- For terminal UIs, resize the window so the full interface fits without wrapping important columns or controls.

When a terminal window screenshot is needed, use the window-control helper described in the main skill to bring the terminal forward and capture only that app window. Do not fall back to a full-desktop screenshot.

## What Counts As Proof

Strong terminal proof includes:

- the executed command or clear runtime context
- the success/failure line, exit status, or final state
- relevant identifiers such as test names, ports, request IDs, migration names, or file paths
- enough timestamp/log context to connect an action to its result when logs are used

Weak proof includes:

- a prompt with no command output
- a partial log excerpt that does not show the outcome
- a screenshot where the important line is wrapped, clipped, hidden, or unreadable
- server output that only proves startup when the requested behavior needed an actual request or interaction

If the terminal evidence does not prove the requested behavior by itself, combine it with a more specific artifact from the relevant strategy.

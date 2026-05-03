# Terminal Evidence

Use when terminal output is the clearest proof of behavior.

Good fits:

- CLI tools, scripts, migrations, generators, and one-off commands
- test, build, lint, typecheck, benchmark, or deployment output
- terminal UIs, prompts, progress, and interactive state
- logs that prove a request, event, job, webhook, or error path happened

Prefer plain captured output for text-only proof. Use a screenshot when terminal
layout or visual state matters.

## Capture Guidance

- Show the command when it matters.
- Let commands finish or reach the stable state being proven.
- Keep enough surrounding output to show cause and effect.
- Trim unrelated or very long output.
- For terminal UIs, avoid wrapping or clipping important content.

## Proof Quality

Strong proof shows the command or context, final state, and relevant identifiers
such as test names, ports, request IDs, migrations, or file paths.

Weak proof is partial, clipped, missing the outcome, or only shows startup when
the requested behavior needed an interaction.

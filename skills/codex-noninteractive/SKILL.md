---
name: codex-noninteractive
description: Use this skill when a user asks for non-trivial code changes — such as implementing a new feature, fixing a bug, refactoring, or any task that requires meaningful edits across one or more files. Skip it for minor changes that can be done in a few lines directly.
allowed-tools: Exec Glob Grep Read
compatibility: Requires Codex CLI
---

# Codex Non-Interactive Mode

## Use this skill for

Use this skill when designing, reviewing, or generating commands and workflows around `codex exec`.

Good matches include CI jobs, release-note generation, repo triage, automated fixes, log summarization, structured JSON output, stdin piping, schema-constrained output, sandbox configuration, API-key handling, and non-interactive session resume.

## Core model

`codex exec` runs Codex without opening the interactive TUI. It is meant for scripts, pipelines, scheduled jobs, and automation.

Default behavior:

- Progress streams to `stderr`.
- The final assistant message prints to `stdout`.
- Piped stdin can be used as task context.
- `codex exec -` makes stdin the full prompt.
- The default sandbox is read-only.
- Runs normally require a Git repository unless `--skip-git-repo-check` is used.
- `codex e` is the short alias for `codex exec`.

## Standard procedure

1. Identify the automation shape: one-off shell command, CI step, JSONL event stream, schema output, or resumed multi-stage run.
2. Choose the safest permission level that can complete the task.
3. Choose the input pattern: prompt only, prompt plus stdin context, or stdin-as-prompt with `-`.
4. Choose output handling: final stdout, `--output-last-message`, JSONL with `--json`, or `--output-schema`.
5. For CI, keep credentials out of untrusted setup/build/test steps.
6. When generating a workflow that writes changes, separate read-only Codex generation from write-permission PR creation.

## Common commands

Prompt only:

```bash
codex exec "summarize the repository structure and list the top 5 risky areas"
```

Redirect final output:

```bash
codex exec "generate release notes for the last 10 commits" | tee release-notes.md
```

Avoid persisted rollout files:

```bash
codex exec --ephemeral "triage this repository and suggest next steps"
```

Allow workspace edits:

```bash
codex exec --sandbox workspace-write "fix the failing tests"
```

Emit JSONL events:

```bash
codex exec --json "summarize the repo structure" | jq
```

Write only the final message to a file:

```bash
codex exec "summarize the repo structure" -o summary.md
```

Resume the last non-interactive session:

```bash
codex exec "review the change for race conditions"
codex exec resume --last "fix the race conditions you found"
```

## Permissions and safety

Use least privilege.

| Need | Command pattern |
|---|---|
| Inspect only | `codex exec "<task>"` |
| Modify workspace files | `codex exec --sandbox workspace-write "<task>"` |
| Broader system access | `codex exec --sandbox danger-full-access "<task>"` |
| No saved rollout files | `codex exec --ephemeral "<task>"` |
| Ignore local user config | `codex exec --ignore-user-config "<task>"` |
| Ignore execpolicy rules | `codex exec --ignore-rules "<task>"` |
| Outside Git repository | `codex exec --skip-git-repo-check "<task>"` |

Avoid `danger-full-access` unless the runner is isolated. Treat `--full-auto` as deprecated compatibility behavior; prefer explicit `--sandbox workspace-write`.

If an enabled MCP server has `required = true` and cannot initialize, expect `codex exec` to exit with an error.

## Input patterns

Prompt plus stdin context:

```bash
npm test 2>&1   | codex exec "summarize the failing tests and propose the smallest likely fix"   | tee test-summary.md
```

Stdin as the full prompt:

```bash
cat prompt.txt | codex exec -
```

Dynamic generated prompt:

```bash
generate_prompt.sh | codex exec - --json > result.jsonl
```

Use prompt-plus-stdin when command output is context and the instruction is known. Use `codex exec -` when another script or file generates the full instruction.

## Structured and machine-readable output

Use `--json` when downstream tooling needs progress/events. JSONL event types include `thread.started`, `turn.started`, `turn.completed`, `turn.failed`, `item.*`, and `error`.

Use `--output-schema` when downstream tooling needs a stable final JSON shape:

```bash
codex exec "Extract project metadata"   --output-schema ./schema.json   -o ./project-metadata.json
```

Keep schemas strict: require needed fields and set `additionalProperties: false`.

## Authentication in automation

`codex exec` reuses saved CLI authentication by default.

For GitHub Actions, prefer `openai/codex-action` over installing the CLI and exposing an API key to a shell step.

Do not set `OPENAI_API_KEY` or `CODEX_API_KEY` as a job-level environment variable in workflows that check out or run repository-controlled code.

For non-GitHub automation, scope `CODEX_API_KEY` to the single `codex exec` invocation:

```bash
CODEX_API_KEY=<api-key> codex exec --json "triage open bug reports"
```

`CODEX_API_KEY` is supported only by `codex exec`.

Treat `~/.codex/auth.json` like a password. Do not use ChatGPT-managed auth files in public or open-source repository runners.

## GitHub Actions autofix pattern

When asked to auto-fix CI failures in GitHub Actions, use this security pattern:

1. Trigger a follow-up workflow from failed CI.
2. Check out the failing commit with `contents: read`.
3. Run setup and tests before exposing OpenAI credentials.
4. Run `openai/codex-action`.
5. Save Codex changes as a patch artifact.
6. In a separate job with write permissions, apply the patch and open a PR.

Read `references/github-actions-autofix.md` when a full workflow is needed.

## Resources

Load these only when the task needs the extra detail:

- `references/codex-exec-flags.md`: full `codex exec` and `codex exec resume` flag table.
- `references/structured-output.md`: JSONL events and schema-constrained output patterns.
- `references/stdin-piping.md`: prompt-plus-stdin and stdin-as-prompt examples.
- `references/auth-and-ci-security.md`: API-key handling and CI auth guidance.
- `references/github-actions-autofix.md`: full secure GitHub Actions auto-fix workflow.
- `references/full-non-interactive-reference.md`: complete cleaned source reference.
- `references/agentskills-practices-used.md`: Agent Skills design choices used in this package.

## Response checklist

Before returning a command or workflow, verify:

- The command uses `codex exec` or `codex exec resume`, not the interactive TUI.
- The sandbox is the least-permissive viable option.
- Secrets are scoped only to the Codex invocation or protected action.
- Untrusted repository code does not run with API keys in its environment.
- Output mode matches the caller's downstream consumer.
- Stdin mode matches whether stdin is context or the full prompt.
- The Git repository requirement is handled explicitly.
